package delivery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/persistence"
)

var (
	// ErrPublicationConflict means the namespaced ref no longer matches the
	// approved compare-and-swap state.
	ErrPublicationConflict = errors.New("delivery publication conflict")
	// ErrPublicationNotApplied means recovery found the approved old ref and
	// therefore cannot claim that publication occurred.
	ErrPublicationNotApplied = errors.New("delivery publication was not applied")
)

// PublicationResult is returned only after the receipt is durable. A release
// failure can accompany a non-zero result without invalidating publication.
type PublicationResult struct {
	Receipt       contracts.DeliveryReceipt
	Replayed      bool
	LeaseReleased bool
}

// PublicationService owns the journal-before-CAS-before-receipt protocol.
type PublicationService struct {
	manager  *WorktreeManager
	journal  persistence.DeliveryPublicationRepository
	leaseSet persistence.LeaseSetRepository
	schemas  *contracts.Registry
}

// NewPublicationService constructs a publication boundary over trusted Git
// and durable persistence adapters.
func NewPublicationService(
	manager *WorktreeManager,
	journal persistence.DeliveryPublicationRepository,
	leaseSet persistence.LeaseSetRepository,
) (*PublicationService, error) {
	if manager == nil || journal == nil || leaseSet == nil {
		return nil, fmt.Errorf("worktree manager, publication journal, and lease repository are required")
	}
	schemas, err := contracts.NewRegistry()
	if err != nil {
		return nil, fmt.Errorf("compile canonical schemas: %w", err)
	}
	return &PublicationService{
		manager: manager, journal: journal, leaseSet: leaseSet, schemas: schemas,
	}, nil
}

// Publish persists an immutable intent, updates only the approved namespaced
// ref with compare-and-swap semantics, persists the receipt, then releases the
// exact lease set.
func (s *PublicationService) Publish(
	ctx context.Context,
	request contracts.DeliveryRequest,
	result CommitResult,
	bundle ValidationBundle,
	leaseSet persistence.LeaseSet,
) (PublicationResult, error) {
	intent, receipt, err := s.publicationIntent(ctx, request, result, bundle, leaseSet)
	if err != nil {
		return PublicationResult{}, err
	}
	// A completed journal row is immutable authority. Check it before requiring
	// the lease to still be live so a process restart can replay an already
	// published result after the original lease was released or expired.
	recorded, found, err := s.journal.GetDeliveryPublication(
		ctx, request.DeliveryID, request.AttemptID,
	)
	if err != nil {
		return PublicationResult{}, err
	}
	if found {
		if err := requireExactPublicationRecord(recorded, intent); err != nil {
			return PublicationResult{}, err
		}
		switch recorded.State {
		case "published":
			observed, err := s.observePublicationRef(ctx, request.RepositoryPath, request.PublicationRef)
			if err != nil {
				return PublicationResult{}, err
			}
			if observed == nil || *observed != result.ResultCommit {
				return PublicationResult{}, fmt.Errorf("%w: durable receipt disagrees with the Git ref", ErrPublicationConflict)
			}
			return s.releasePublishedLease(ctx, receipt, leaseSet, true)
		case "conflict", "abandoned":
			return PublicationResult{}, fmt.Errorf("%w: publication attempt is already %s", ErrPublicationConflict, recorded.State)
		case "prepared":
		default:
			return PublicationResult{}, fmt.Errorf("publication journal returned unsupported state %q", recorded.State)
		}
	}
	record, err := s.journal.PrepareDeliveryPublication(ctx, intent, leaseSet)
	if err != nil {
		return PublicationResult{}, err
	}
	if err := requireExactPublicationRecord(record, intent); err != nil {
		return PublicationResult{}, err
	}
	if record.State == "conflict" {
		return PublicationResult{}, fmt.Errorf("%w: prepared publication is already conflicted", ErrPublicationConflict)
	}
	if record.State == "published" {
		observed, err := s.observePublicationRef(ctx, request.RepositoryPath, request.PublicationRef)
		if err != nil {
			return PublicationResult{}, err
		}
		if observed == nil || *observed != result.ResultCommit {
			return PublicationResult{}, fmt.Errorf("%w: durable receipt disagrees with the Git ref", ErrPublicationConflict)
		}
		return s.releasePublishedLease(ctx, receipt, leaseSet, true)
	}
	if record.State != "prepared" {
		return PublicationResult{}, fmt.Errorf("publication journal returned unsupported state %q", record.State)
	}

	record, err = s.journal.CompleteDeliveryPublication(
		ctx, intent, leaseSet,
		func(applyContext context.Context) error {
			return s.applyPublicationCAS(applyContext, request, intent)
		},
	)
	if err != nil {
		var refConflict *publicationRefConflict
		if errors.As(err, &refConflict) {
			_, journalErr := s.journal.ConflictDeliveryPublication(
				ctx, intent, refConflict.observed,
			)
			return PublicationResult{}, errors.Join(refConflict.cause, journalErr)
		}
		return PublicationResult{}, fmt.Errorf("persist published delivery receipt: %w", err)
	}
	if record.State != "published" {
		return PublicationResult{}, fmt.Errorf("publication journal did not persist a published receipt")
	}
	return s.releasePublishedLease(ctx, receipt, leaseSet, false)
}

// Recover finalizes only an exact prepared intent when the trusted Git read
// independently proves that the CAS result is already present.
func (s *PublicationService) Recover(
	ctx context.Context,
	request contracts.DeliveryRequest,
	result CommitResult,
	bundle ValidationBundle,
	leaseSet persistence.LeaseSet,
) (PublicationResult, error) {
	intent, receipt, err := s.publicationIntent(ctx, request, result, bundle, leaseSet)
	if err != nil {
		return PublicationResult{}, err
	}
	record, found, err := s.journal.GetDeliveryPublication(
		ctx, request.DeliveryID, request.AttemptID,
	)
	if err != nil {
		return PublicationResult{}, err
	}
	if !found {
		return PublicationResult{}, fmt.Errorf("publication intent was not durably prepared")
	}
	if err := requireExactPublicationRecord(record, intent); err != nil {
		return PublicationResult{}, err
	}
	observed, err := s.observePublicationRef(ctx, request.RepositoryPath, request.PublicationRef)
	if err != nil {
		return PublicationResult{}, err
	}
	if record.State == "published" {
		if observed == nil || *observed != result.ResultCommit {
			return PublicationResult{}, fmt.Errorf("%w: durable receipt disagrees with the Git ref", ErrPublicationConflict)
		}
		return s.releasePublishedLease(ctx, receipt, leaseSet, true)
	}
	if record.State != "prepared" {
		return PublicationResult{}, fmt.Errorf("publication in state %q cannot be recovered", record.State)
	}
	if observed != nil && *observed == result.ResultCommit {
		record, err = s.journal.RecoverDeliveryPublication(ctx, intent, *observed)
		if err != nil {
			return PublicationResult{}, err
		}
		if record.State != "published" {
			return PublicationResult{}, fmt.Errorf("recovery did not persist a published receipt")
		}
		return s.releasePublishedLease(ctx, receipt, leaseSet, true)
	}
	if optionalCommitEqual(observed, request.PublicationPreviousCommit) {
		return PublicationResult{}, ErrPublicationNotApplied
	}
	if _, conflictErr := s.journal.ConflictDeliveryPublication(ctx, intent, observed); conflictErr != nil {
		return PublicationResult{}, errors.Join(
			fmt.Errorf("%w: publication ref moved to an unapproved commit", ErrPublicationConflict),
			conflictErr,
		)
	}
	return PublicationResult{}, fmt.Errorf("%w: publication ref moved to an unapproved commit", ErrPublicationConflict)
}

func (s *PublicationService) releasePublishedLease(
	ctx context.Context,
	receipt contracts.DeliveryReceipt,
	leaseSet persistence.LeaseSet,
	replayed bool,
) (PublicationResult, error) {
	outcome := PublicationResult{Receipt: receipt, Replayed: replayed}
	if err := s.leaseSet.ReleaseLeaseSet(ctx, leaseSet); err != nil {
		return outcome, fmt.Errorf("published receipt is durable but lease release failed: %w", err)
	}
	outcome.LeaseReleased = true
	return outcome, nil
}

func (s *PublicationService) publicationIntent(
	ctx context.Context,
	request contracts.DeliveryRequest,
	result CommitResult,
	bundle ValidationBundle,
	leaseSet persistence.LeaseSet,
) (persistence.DeliveryPublicationIntent, contracts.DeliveryReceipt, error) {
	if err := contracts.ValidateDeliveryRequest(request); err != nil {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, err
	}
	if leaseSet.LeaseSetID != request.AttemptID {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, fmt.Errorf("lease set ID must equal the approved attempt ID")
	}
	resolved, err := s.manager.resolveRequest(ctx, request)
	if err != nil {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, err
	}
	recomputed, err := s.manager.inspectCommitResult(ctx, resolved, result.ResultCommit)
	if err != nil {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, err
	}
	if !sameCommitResult(result, recomputed) {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, fmt.Errorf("supplied result identity disagrees with Git")
	}
	fences, err := receiptFences(request, leaseSet)
	if err != nil {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, err
	}
	receipt := contracts.DeliveryReceipt{
		DeliveryID: request.DeliveryID, SchemaVersion: "1.0", Status: "published",
		BaseCommit: result.BaseCommit, ResultCommit: result.ResultCommit,
		ResultTree: result.ResultTree, PatchSHA256: result.PatchSHA256,
		ChangedPaths:              append([]string(nil), result.ChangedPaths...),
		PublicationRef:            request.PublicationRef,
		PublicationPreviousCommit: cloneOptionalString(request.PublicationPreviousCommit),
		AuthorID:                  request.AuthorID, ValidatorID: request.ValidatorID,
		LeaseFences: fences, ValidationReportRef: bundle.ReportRef,
		EvidenceManifestRef: bundle.ManifestRef,
		CreatedAt:           bundle.Report.CreatedAt, PublishedAt: bundle.Report.CreatedAt,
	}
	if err := contracts.ValidateDeliveryPublication(
		request, bundle.Report, bundle.ManifestJSON, receipt,
	); err != nil {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, err
	}
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, fmt.Errorf("encode canonical receipt: %w", err)
	}
	if err := s.schemas.ValidateJSON("delivery-receipt.schema.json", receiptJSON); err != nil {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, fmt.Errorf("validate canonical receipt schema: %w", err)
	}
	authorityJSON, err := json.Marshal(request)
	if err != nil {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, fmt.Errorf("encode publication authority: %w", err)
	}
	authorityDigest := sha256.Sum256(authorityJSON)
	receiptDigest := sha256.Sum256(receiptJSON)
	identityDocument := struct {
		DeliveryID      string `json:"delivery_id"`
		AttemptID       string `json:"attempt_id"`
		LeaseSetID      string `json:"lease_set_id"`
		AuthoritySHA256 string `json:"authority_sha256"`
		ReceiptSHA256   string `json:"receipt_sha256"`
	}{
		request.DeliveryID, request.AttemptID, leaseSet.LeaseSetID,
		fmt.Sprintf("%x", authorityDigest), fmt.Sprintf("%x", receiptDigest),
	}
	identityJSON, err := json.Marshal(identityDocument)
	if err != nil {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, fmt.Errorf("encode publication intent identity: %w", err)
	}
	intentDigest := sha256.Sum256(identityJSON)
	return persistence.DeliveryPublicationIntent{
		DeliveryID: request.DeliveryID, AttemptID: request.AttemptID,
		LeaseSetID: leaseSet.LeaseSetID, PublicationRef: request.PublicationRef,
		PublicationPreviousCommit: cloneOptionalString(request.PublicationPreviousCommit),
		ResultCommit:              result.ResultCommit,
		AuthoritySHA256:           fmt.Sprintf("%x", authorityDigest),
		ReceiptSHA256:             fmt.Sprintf("%x", receiptDigest),
		IntentSHA256:              fmt.Sprintf("%x", intentDigest), ReceiptJSON: receiptJSON,
	}, receipt, nil
}

func receiptFences(
	request contracts.DeliveryRequest,
	leaseSet persistence.LeaseSet,
) ([]contracts.DeliveryLeaseFence, error) {
	if leaseSet.OwnerID == "" || len(leaseSet.Leases) == 0 {
		return nil, fmt.Errorf("lease set has no ownership proof")
	}
	fences := make([]contracts.DeliveryLeaseFence, 0, len(leaseSet.Leases))
	for _, lease := range leaseSet.Leases {
		if lease.OwnerID != leaseSet.OwnerID || lease.FencingToken < 1 {
			return nil, fmt.Errorf("lease set contains inconsistent ownership proof")
		}
		fences = append(fences, contracts.DeliveryLeaseFence{
			ResourceType: lease.ResourceType, ResourceID: lease.ResourceID,
			OwnerID: lease.OwnerID, FencingToken: lease.FencingToken,
		})
	}
	slices.SortFunc(fences, func(left, right contracts.DeliveryLeaseFence) int {
		if value := strings.Compare(left.ResourceType, right.ResourceType); value != 0 {
			return value
		}
		return strings.Compare(left.ResourceID, right.ResourceID)
	})
	keys := make([]string, 0, len(fences))
	for _, fence := range fences {
		keys = append(keys, fence.ResourceType+"\x00"+fence.ResourceID)
	}
	if !slices.Equal(keys, contracts.ExpectedDeliveryFenceKeys(request)) {
		return nil, fmt.Errorf("lease set fences disagree with approved delivery scopes")
	}
	return fences, nil
}

func (s *PublicationService) applyPublicationCAS(
	ctx context.Context,
	request contracts.DeliveryRequest,
	intent persistence.DeliveryPublicationIntent,
) error {
	observed, err := s.observePublicationRef(ctx, request.RepositoryPath, request.PublicationRef)
	if err != nil {
		return err
	}
	if observed != nil && *observed == intent.ResultCommit {
		return nil
	}
	if !optionalCommitEqual(observed, request.PublicationPreviousCommit) {
		return &publicationRefConflict{
			observed: cloneOptionalString(observed),
			cause: fmt.Errorf(
				"%w: publication ref is not at the approved previous commit",
				ErrPublicationConflict,
			),
		}
	}
	oldCommit := strings.Repeat("0", 40)
	if request.PublicationPreviousCommit != nil {
		oldCommit = *request.PublicationPreviousCommit
	}
	_, updateErr := s.manager.gitMutation(
		ctx, request.RepositoryPath,
		"update-ref", "--no-deref", request.PublicationRef, intent.ResultCommit, oldCommit,
	)
	if updateErr == nil {
		return nil
	}
	observed, observeErr := s.observePublicationRef(ctx, request.RepositoryPath, request.PublicationRef)
	if observeErr == nil && observed != nil && *observed == intent.ResultCommit {
		return nil
	}
	if observeErr != nil {
		return errors.Join(fmt.Errorf("compare-and-swap publication: %w", updateErr), observeErr)
	}
	if optionalCommitEqual(observed, request.PublicationPreviousCommit) {
		return fmt.Errorf("compare-and-swap publication did not complete: %w", updateErr)
	}
	return &publicationRefConflict{
		observed: cloneOptionalString(observed),
		cause: fmt.Errorf(
			"%w: publication ref moved during compare-and-swap",
			ErrPublicationConflict,
		),
	}
}

type publicationRefConflict struct {
	observed *string
	cause    error
}

func (e *publicationRefConflict) Error() string {
	return e.cause.Error()
}

func (e *publicationRefConflict) Unwrap() error {
	return e.cause
}

func (s *PublicationService) observePublicationRef(
	ctx context.Context,
	repository string,
	publicationRef string,
) (*string, error) {
	output, err := s.manager.git(
		ctx, repository,
		"for-each-ref", "--format=%(refname)%00%(objecttype)%00%(objectname)%00%(symref)",
		"--count=2", publicationRef,
	)
	if err != nil {
		return nil, fmt.Errorf("inspect publication ref: %w", err)
	}
	output = bytes.TrimSpace(output)
	if len(output) == 0 {
		return nil, nil
	}
	lines := bytes.Split(output, []byte{'\n'})
	if len(lines) != 1 {
		return nil, fmt.Errorf("publication ref lookup was ambiguous")
	}
	fields := bytes.Split(lines[0], []byte{0})
	if len(fields) != 4 || string(fields[0]) != publicationRef ||
		string(fields[1]) != "commit" || len(fields[3]) != 0 ||
		!fullObjectIDPattern.Match(fields[2]) {
		return nil, fmt.Errorf("publication ref is symbolic or does not identify a canonical commit")
	}
	value := string(fields[2])
	return &value, nil
}

func requireExactPublicationRecord(
	record persistence.DeliveryPublication,
	intent persistence.DeliveryPublicationIntent,
) error {
	stored := record.Intent
	if stored.DeliveryID != intent.DeliveryID || stored.AttemptID != intent.AttemptID ||
		stored.LeaseSetID != intent.LeaseSetID || stored.PublicationRef != intent.PublicationRef ||
		!optionalCommitEqual(stored.PublicationPreviousCommit, intent.PublicationPreviousCommit) ||
		stored.ResultCommit != intent.ResultCommit || stored.AuthoritySHA256 != intent.AuthoritySHA256 ||
		stored.ReceiptSHA256 != intent.ReceiptSHA256 || stored.IntentSHA256 != intent.IntentSHA256 ||
		!bytes.Equal(stored.ReceiptJSON, intent.ReceiptJSON) {
		return fmt.Errorf("%w: publication intent replay changed authority", ErrPublicationConflict)
	}
	return nil
}

func optionalCommitEqual(left *string, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
