package delivery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

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

// RepositoryAuthority binds one publication service instance to one logical
// repository and its operator-configured canonical Git checkout.
type RepositoryAuthority struct {
	TenantID       string
	RepositoryID   string
	RepositoryPath string
}

// PublicationService owns the journal-before-CAS-before-receipt protocol.
type PublicationService struct {
	manager            *WorktreeManager
	journal            persistence.DeliveryPublicationRepository
	leaseSet           persistence.LeaseSetRepository
	schemas            *contracts.Registry
	authority          RepositoryAuthority
	repositoryIdentity os.FileInfo
	now                func() time.Time
}

// NewPublicationService constructs a publication boundary over trusted Git
// and durable persistence adapters.
func NewPublicationService(
	manager *WorktreeManager,
	journal persistence.DeliveryPublicationRepository,
	leaseSet persistence.LeaseSetRepository,
	authority RepositoryAuthority,
) (*PublicationService, error) {
	if manager == nil || journal == nil || leaseSet == nil {
		return nil, fmt.Errorf("worktree manager, publication journal, and lease repository are required")
	}
	if err := contracts.ValidateRepositoryIdentity(authority.TenantID, authority.RepositoryID); err != nil {
		return nil, fmt.Errorf("repository authority: %w", err)
	}
	physicalRepository, err := canonicalDirectory(authority.RepositoryPath, "repository authority")
	if err != nil {
		return nil, err
	}
	repositoryIdentity, err := os.Stat(physicalRepository)
	if err != nil {
		return nil, fmt.Errorf("stat repository authority: %w", err)
	}
	authority.RepositoryPath = physicalRepository
	schemas, err := contracts.NewRegistry()
	if err != nil {
		return nil, fmt.Errorf("compile canonical schemas: %w", err)
	}
	return &PublicationService{
		manager: manager, journal: journal, leaseSet: leaseSet, schemas: schemas,
		authority: authority, repositoryIdentity: repositoryIdentity, now: time.Now,
	}, nil
}

func (s *PublicationService) verifyRepositoryAuthority() error {
	physicalRepository, err := canonicalDirectory(s.authority.RepositoryPath, "repository authority")
	if err != nil {
		return err
	}
	currentIdentity, err := os.Stat(physicalRepository)
	if err != nil {
		return fmt.Errorf("stat repository authority: %w", err)
	}
	if physicalRepository != s.authority.RepositoryPath ||
		!os.SameFile(s.repositoryIdentity, currentIdentity) {
		return fmt.Errorf("publication service repository authority changed on disk")
	}
	return nil
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
	// A completed journal row is immutable authority. Check it before requiring
	// the lease to still be live so a process restart can replay an already
	// published result after the original lease was released or expired.
	recorded, found, err := s.journal.GetDeliveryPublication(
		ctx, request.DeliveryID, request.AttemptID,
	)
	if err != nil {
		return PublicationResult{}, err
	}
	publicationAt := s.now().UTC().Truncate(time.Microsecond)
	receiptTimes := publicationReceiptTimes{CreatedAt: publicationAt, PublishedAt: publicationAt}
	if found {
		receiptTimes, err = recordedPublicationTimes(recorded)
		if err != nil {
			return PublicationResult{}, err
		}
	}
	requirePersistedEvidence := !found || recorded.State == "prepared"
	intent, receipt, err := s.publicationIntent(
		ctx, request, result, bundle, leaseSet, receiptTimes, requirePersistedEvidence,
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
			observed, err := s.observePublicationRef(ctx, request.PublicationRef)
			if err != nil {
				return PublicationResult{}, publicationRefReadError(err)
			}
			if observed == nil || *observed != result.ResultCommit {
				return PublicationResult{}, fmt.Errorf("%w: durable receipt disagrees with the Git ref", ErrPublicationConflict)
			}
			return s.releasePublishedLease(ctx, receipt, leaseSet, true)
		case "abandoned":
			if err := s.leaseSet.ReleaseLeaseSet(ctx, leaseSet); err != nil {
				return PublicationResult{}, errors.Join(ErrPublicationNotApplied, err)
			}
			return PublicationResult{}, ErrPublicationNotApplied
		case "conflict":
			return PublicationResult{}, fmt.Errorf("%w: publication attempt is already %s", ErrPublicationConflict, recorded.State)
		case "prepared":
		default:
			return PublicationResult{}, fmt.Errorf("publication journal returned unsupported state %q", recorded.State)
		}
	}
	record, err := s.journal.PrepareDeliveryPublication(ctx, intent, leaseSet)
	if err != nil {
		// Two exact first publishers can choose different operation timestamps
		// before either observes the prepared row. The first durable timestamp is
		// canonical; rebuild against it and accept only an otherwise exact intent.
		concurrent, concurrentFound, loadErr := s.journal.GetDeliveryPublication(
			ctx, request.DeliveryID, request.AttemptID,
		)
		if loadErr != nil {
			return PublicationResult{}, errors.Join(err, loadErr)
		}
		if !concurrentFound {
			return PublicationResult{}, err
		}
		concurrentTimes, timestampErr := recordedPublicationTimes(concurrent)
		if timestampErr != nil {
			return PublicationResult{}, errors.Join(err, timestampErr)
		}
		concurrentIntent, concurrentReceipt, rebuildErr := s.publicationIntent(
			ctx, request, result, bundle, leaseSet, concurrentTimes,
			concurrent.State == "prepared",
		)
		if rebuildErr != nil {
			return PublicationResult{}, errors.Join(err, rebuildErr)
		}
		if exactErr := requireExactPublicationRecord(concurrent, concurrentIntent); exactErr != nil {
			return PublicationResult{}, errors.Join(err, exactErr)
		}
		intent, receipt, record = concurrentIntent, concurrentReceipt, concurrent
	}
	if err := requireExactPublicationRecord(record, intent); err != nil {
		return PublicationResult{}, err
	}
	if record.State == "conflict" {
		return PublicationResult{}, fmt.Errorf("%w: prepared publication is already conflicted", ErrPublicationConflict)
	}
	if record.State == "published" {
		observed, err := s.observePublicationRef(ctx, request.PublicationRef)
		if err != nil {
			return PublicationResult{}, publicationRefReadError(err)
		}
		if observed == nil || *observed != result.ResultCommit {
			return PublicationResult{}, fmt.Errorf("%w: durable receipt disagrees with the Git ref", ErrPublicationConflict)
		}
		return s.releasePublishedLease(ctx, receipt, leaseSet, true)
	}
	if record.State == "abandoned" {
		if err := s.leaseSet.ReleaseLeaseSet(ctx, leaseSet); err != nil {
			return PublicationResult{}, errors.Join(ErrPublicationNotApplied, err)
		}
		return PublicationResult{}, ErrPublicationNotApplied
	}
	if record.State != "prepared" {
		return PublicationResult{}, fmt.Errorf("publication journal returned unsupported state %q", record.State)
	}

	callbackRan := false
	record, err = s.journal.CompleteDeliveryPublication(
		ctx, intent, leaseSet,
		func(context.Context) error {
			if err := verifyPersistedValidationBundle(request, bundle); err != nil {
				return fmt.Errorf("revalidate persisted publication evidence: %w", err)
			}
			return nil
		},
		func(applyContext context.Context) error {
			callbackRan = true
			return s.applyPublicationCAS(applyContext, intent)
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
	observed, err := s.observePublicationRef(ctx, request.PublicationRef)
	if err != nil {
		return PublicationResult{}, publicationRefReadError(err)
	}
	if observed == nil || *observed != result.ResultCommit {
		return PublicationResult{}, fmt.Errorf("%w: durable receipt disagrees with the Git ref", ErrPublicationConflict)
	}
	return s.releasePublishedLease(ctx, receipt, leaseSet, !callbackRan)
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
	record, found, err := s.journal.GetDeliveryPublication(
		ctx, request.DeliveryID, request.AttemptID,
	)
	if err != nil {
		return PublicationResult{}, err
	}
	publicationAt := s.now().UTC().Truncate(time.Microsecond)
	receiptTimes := publicationReceiptTimes{CreatedAt: publicationAt, PublishedAt: publicationAt}
	if found {
		receiptTimes, err = recordedPublicationTimes(record)
		if err != nil {
			return PublicationResult{}, err
		}
	}
	intent, receipt, err := s.publicationIntent(
		ctx, request, result, bundle, leaseSet, receiptTimes, false,
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
	if record.State == "abandoned" {
		if err := s.leaseSet.ReleaseLeaseSet(ctx, leaseSet); err != nil {
			return PublicationResult{}, errors.Join(ErrPublicationNotApplied, err)
		}
		return PublicationResult{}, ErrPublicationNotApplied
	}
	observed, err := s.observePublicationRef(ctx, request.PublicationRef)
	if err != nil {
		if record.State == "prepared" {
			return PublicationResult{}, s.recordInvalidPublicationRefConflict(ctx, intent, err)
		}
		return PublicationResult{}, publicationRefReadError(err)
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
		observed, err = s.observePublicationRef(ctx, request.PublicationRef)
		if err != nil {
			return PublicationResult{}, publicationRefReadError(err)
		}
		if observed == nil || *observed != result.ResultCommit {
			return PublicationResult{}, fmt.Errorf("%w: recovered receipt disagrees with the Git ref", ErrPublicationConflict)
		}
		return s.releasePublishedLease(ctx, receipt, leaseSet, true)
	}
	if optionalCommitEqual(observed, intent.PublicationPreviousCommit) {
		record, err = s.journal.AbandonDeliveryPublication(
			ctx, intent,
			func(observeContext context.Context) (*string, error) {
				return s.observePublicationRef(observeContext, request.PublicationRef)
			},
		)
		if err != nil {
			return PublicationResult{}, s.recordInvalidPublicationRefConflict(ctx, intent, err)
		}
		switch record.State {
		case "published":
			observed, err = s.observePublicationRef(ctx, request.PublicationRef)
			if err != nil {
				return PublicationResult{}, publicationRefReadError(err)
			}
			if observed == nil || *observed != result.ResultCommit {
				return PublicationResult{}, fmt.Errorf("%w: durable receipt disagrees with the Git ref", ErrPublicationConflict)
			}
			return s.releasePublishedLease(ctx, receipt, leaseSet, true)
		case "abandoned":
			if err := s.leaseSet.ReleaseLeaseSet(ctx, leaseSet); err != nil {
				return PublicationResult{}, errors.Join(ErrPublicationNotApplied, err)
			}
			return PublicationResult{}, ErrPublicationNotApplied
		default:
			return PublicationResult{}, fmt.Errorf("publication abandonment returned unsupported state %q", record.State)
		}
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
	receiptTimes publicationReceiptTimes,
	requirePersistedEvidence bool,
) (persistence.DeliveryPublicationIntent, contracts.DeliveryReceipt, error) {
	if err := s.verifyRepositoryAuthority(); err != nil {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, err
	}
	if err := contracts.ValidateDeliveryRequest(request); err != nil {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, err
	}
	physicalRepository, err := canonicalDirectory(request.RepositoryPath, "delivery repository")
	if err != nil {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, err
	}
	if request.TenantID != s.authority.TenantID || request.RepositoryID != s.authority.RepositoryID ||
		physicalRepository != s.authority.RepositoryPath {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, fmt.Errorf(
			"delivery request does not match the publication service repository authority",
		)
	}
	storageTenantID, storageRepositoryID, err := contracts.RepositoryStorageIdentity(
		request.TenantID, request.RepositoryID,
	)
	if err != nil {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, err
	}
	if leaseSet.LeaseSetID != request.AttemptID {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, fmt.Errorf("lease set ID must equal the approved attempt ID")
	}
	authorizedRequest := request
	authorizedRequest.RepositoryPath = s.authority.RepositoryPath
	resolved, err := s.manager.resolveRequest(ctx, authorizedRequest)
	if err != nil {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, err
	}
	if resolved.repositoryPhysical != s.authority.RepositoryPath {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, fmt.Errorf(
			"resolved delivery repository changed after authority validation",
		)
	}
	recomputed, err := s.manager.inspectCommitResult(ctx, resolved, result.ResultCommit)
	if err != nil {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, err
	}
	if err := s.verifyRepositoryAuthority(); err != nil {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, err
	}
	if !sameCommitResult(result, recomputed) {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, fmt.Errorf("supplied result identity disagrees with Git")
	}
	fences, err := receiptFences(request, leaseSet, storageTenantID, storageRepositoryID)
	if err != nil {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, err
	}
	receipt := contracts.DeliveryReceipt{
		DeliveryID: request.DeliveryID, TenantID: request.TenantID,
		RepositoryID: request.RepositoryID, SchemaVersion: contracts.DeliverySchemaVersion, Status: "published",
		BaseCommit: result.BaseCommit, ResultCommit: result.ResultCommit,
		ResultTree: result.ResultTree, PatchSHA256: result.PatchSHA256,
		ChangedPaths:              append([]string(nil), result.ChangedPaths...),
		PublicationRef:            request.PublicationRef,
		PublicationPreviousCommit: cloneOptionalString(request.PublicationPreviousCommit),
		AuthorID:                  request.AuthorID, ValidatorID: request.ValidatorID,
		LeaseFences: fences, ValidationReportRef: bundle.ReportRef,
		EvidenceManifestRef: bundle.ManifestRef,
		CreatedAt:           receiptTimes.CreatedAt, PublishedAt: receiptTimes.PublishedAt,
	}
	if err := contracts.ValidateDeliveryPublication(
		request, bundle.Report, bundle.ManifestJSON, receipt,
	); err != nil {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, err
	}
	authorityJSON, err := s.validatePublicationArtifactSchemas(request, bundle)
	if err != nil {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, err
	}
	if requirePersistedEvidence {
		if err := verifyPersistedValidationBundle(request, bundle); err != nil {
			return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, fmt.Errorf(
				"verify persisted publication evidence: %w", err,
			)
		}
	}
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, fmt.Errorf("encode canonical receipt: %w", err)
	}
	if err := s.schemas.ValidateJSON("delivery-receipt.schema.json", receiptJSON); err != nil {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, fmt.Errorf(
			"validate canonical receipt schema: %w", err,
		)
	}
	authorityDigest := sha256.Sum256(authorityJSON)
	receiptDigest := sha256.Sum256(receiptJSON)
	identityDocument := struct {
		DeliveryID      string `json:"delivery_id"`
		TenantID        string `json:"tenant_id"`
		RepositoryID    string `json:"repository_id"`
		AttemptID       string `json:"attempt_id"`
		LeaseSetID      string `json:"lease_set_id"`
		LeaseTTLMS      int    `json:"lease_ttl_ms"`
		AuthoritySHA256 string `json:"authority_sha256"`
		ReceiptSHA256   string `json:"receipt_sha256"`
	}{
		request.DeliveryID, storageTenantID, storageRepositoryID,
		request.AttemptID, leaseSet.LeaseSetID, request.LeaseTTLMS,
		fmt.Sprintf("%x", authorityDigest), fmt.Sprintf("%x", receiptDigest),
	}
	identityJSON, err := json.Marshal(identityDocument)
	if err != nil {
		return persistence.DeliveryPublicationIntent{}, contracts.DeliveryReceipt{}, fmt.Errorf("encode publication intent identity: %w", err)
	}
	intentDigest := sha256.Sum256(identityJSON)
	return persistence.DeliveryPublicationIntent{
		DeliveryID: request.DeliveryID, TenantID: storageTenantID,
		RepositoryID: storageRepositoryID, AttemptID: request.AttemptID,
		LeaseSetID: leaseSet.LeaseSetID, LeaseTTLMS: request.LeaseTTLMS,
		PublicationRef:            request.PublicationRef,
		PublicationPreviousCommit: cloneOptionalString(request.PublicationPreviousCommit),
		ResultCommit:              result.ResultCommit,
		AuthoritySHA256:           fmt.Sprintf("%x", authorityDigest),
		ReceiptSHA256:             fmt.Sprintf("%x", receiptDigest),
		IntentSHA256:              fmt.Sprintf("%x", intentDigest), ReceiptJSON: receiptJSON,
	}, receipt, nil
}

func (s *PublicationService) validatePublicationArtifactSchemas(
	request contracts.DeliveryRequest,
	bundle ValidationBundle,
) ([]byte, error) {
	authorityJSON, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode publication authority: %w", err)
	}
	if err := s.schemas.ValidateJSON("delivery-request.schema.json", authorityJSON); err != nil {
		return nil, fmt.Errorf("validate canonical delivery request schema: %w", err)
	}
	canonicalReport, err := json.Marshal(bundle.Report)
	if err != nil || !bytes.Equal(canonicalReport, bundle.ReportJSON) {
		return nil, fmt.Errorf("validation report bytes are not canonical")
	}
	if err := s.schemas.ValidateJSON("validation-report.schema.json", bundle.ReportJSON); err != nil {
		return nil, fmt.Errorf("validate canonical validation report schema: %w", err)
	}
	canonicalManifest, err := json.Marshal(bundle.Manifest)
	if err != nil || !bytes.Equal(canonicalManifest, bundle.ManifestJSON) {
		return nil, fmt.Errorf("evidence manifest bytes are not canonical")
	}
	if err := s.schemas.ValidateJSON("evidence-manifest.schema.json", bundle.ManifestJSON); err != nil {
		return nil, fmt.Errorf("validate canonical evidence manifest schema: %w", err)
	}
	return authorityJSON, nil
}

type publicationReceiptTimes struct {
	CreatedAt   time.Time
	PublishedAt time.Time
}

func recordedPublicationTimes(
	record persistence.DeliveryPublication,
) (publicationReceiptTimes, error) {
	var receipt contracts.DeliveryReceipt
	if err := json.Unmarshal(record.Intent.ReceiptJSON, &receipt); err != nil {
		return publicationReceiptTimes{}, fmt.Errorf("decode recorded publication receipt timestamp: %w", err)
	}
	if receipt.CreatedAt.IsZero() || receipt.PublishedAt.IsZero() ||
		receipt.PublishedAt.Before(receipt.CreatedAt) {
		return publicationReceiptTimes{}, fmt.Errorf("recorded publication receipt has inconsistent operation timestamps")
	}
	return publicationReceiptTimes{
		CreatedAt: receipt.CreatedAt, PublishedAt: receipt.PublishedAt,
	}, nil
}

func recordedPublicationTime(record persistence.DeliveryPublication) (time.Time, error) {
	timestamps, err := recordedPublicationTimes(record)
	if err != nil {
		return time.Time{}, err
	}
	return timestamps.PublishedAt, nil
}

func receiptFences(
	request contracts.DeliveryRequest,
	leaseSet persistence.LeaseSet,
	storageTenantID string,
	storageRepositoryID string,
) ([]contracts.DeliveryLeaseFence, error) {
	if leaseSet.OwnerID == "" || len(leaseSet.Leases) == 0 {
		return nil, fmt.Errorf("lease set has no ownership proof")
	}
	fences := make([]contracts.DeliveryLeaseFence, 0, len(leaseSet.Leases))
	for _, lease := range leaseSet.Leases {
		if lease.OwnerID != leaseSet.OwnerID || lease.FencingToken < 1 ||
			lease.TenantID != storageTenantID || lease.RepositoryID != storageRepositoryID {
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
	intent persistence.DeliveryPublicationIntent,
) error {
	observed, err := s.observePublicationRef(ctx, intent.PublicationRef)
	if err != nil {
		if conflict, invalid := invalidPublicationRefConflict(err); invalid {
			return conflict
		}
		return err
	}
	if observed != nil && *observed == intent.ResultCommit {
		return nil
	}
	if !optionalCommitEqual(observed, intent.PublicationPreviousCommit) {
		return &publicationRefConflict{
			observed: cloneOptionalString(observed),
			cause: fmt.Errorf(
				"%w: publication ref is not at the approved previous commit",
				ErrPublicationConflict,
			),
		}
	}
	oldCommit := strings.Repeat("0", 40)
	if intent.PublicationPreviousCommit != nil {
		oldCommit = *intent.PublicationPreviousCommit
	}
	if err := s.verifyRepositoryAuthority(); err != nil {
		return err
	}
	_, updateErr := s.manager.gitMutation(
		ctx, s.authority.RepositoryPath,
		"update-ref", "--no-deref", intent.PublicationRef, intent.ResultCommit, oldCommit,
	)
	if updateErr == nil {
		return s.verifyRepositoryAuthority()
	}
	observed, observeErr := s.observePublicationRef(ctx, intent.PublicationRef)
	if observeErr == nil && observed != nil && *observed == intent.ResultCommit {
		return nil
	}
	if observeErr != nil {
		if conflict, invalid := invalidPublicationRefConflict(observeErr); invalid {
			return conflict
		}
		return errors.Join(fmt.Errorf("compare-and-swap publication: %w", updateErr), observeErr)
	}
	if optionalCommitEqual(observed, intent.PublicationPreviousCommit) {
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

type invalidPublicationRefObservation struct {
	cause error
}

func (e *invalidPublicationRefObservation) Error() string {
	return e.cause.Error()
}

func (e *invalidPublicationRefObservation) Unwrap() error {
	return e.cause
}

func invalidPublicationRefConflict(err error) (*publicationRefConflict, bool) {
	var invalid *invalidPublicationRefObservation
	if !errors.As(err, &invalid) {
		return nil, false
	}
	return &publicationRefConflict{
		cause: fmt.Errorf("%w: %v", ErrPublicationConflict, invalid),
	}, true
}

func publicationRefReadError(err error) error {
	if conflict, invalid := invalidPublicationRefConflict(err); invalid {
		return conflict.cause
	}
	return err
}

func (s *PublicationService) recordInvalidPublicationRefConflict(
	ctx context.Context,
	intent persistence.DeliveryPublicationIntent,
	err error,
) error {
	conflict, invalid := invalidPublicationRefConflict(err)
	if !invalid {
		return err
	}
	_, journalErr := s.journal.ConflictDeliveryPublication(ctx, intent, nil)
	return errors.Join(conflict.cause, journalErr)
}

func (e *publicationRefConflict) Error() string {
	return e.cause.Error()
}

func (e *publicationRefConflict) Unwrap() error {
	return e.cause
}

func (s *PublicationService) observePublicationRef(
	ctx context.Context,
	publicationRef string,
) (*string, error) {
	if err := s.verifyRepositoryAuthority(); err != nil {
		return nil, err
	}
	output, err := s.manager.git(
		ctx, s.authority.RepositoryPath,
		"for-each-ref", "--format=%(refname)%00%(objecttype)%00%(objectname)%00%(symref)",
		"--count=2", publicationRef,
	)
	if err != nil {
		return nil, fmt.Errorf("inspect publication ref: %w", err)
	}
	if err := s.verifyRepositoryAuthority(); err != nil {
		return nil, err
	}
	output = bytes.TrimSpace(output)
	if len(output) == 0 {
		return nil, nil
	}
	lines := bytes.Split(output, []byte{'\n'})
	if len(lines) != 1 {
		return nil, &invalidPublicationRefObservation{
			cause: fmt.Errorf("publication ref lookup was ambiguous"),
		}
	}
	fields := bytes.Split(lines[0], []byte{0})
	if len(fields) != 4 || string(fields[0]) != publicationRef ||
		string(fields[1]) != "commit" || len(fields[3]) != 0 ||
		!fullObjectIDPattern.Match(fields[2]) {
		return nil, &invalidPublicationRefObservation{
			cause: fmt.Errorf("publication ref is symbolic or does not identify a canonical commit"),
		}
	}
	value := string(fields[2])
	return &value, nil
}

func requireExactPublicationRecord(
	record persistence.DeliveryPublication,
	intent persistence.DeliveryPublicationIntent,
) error {
	stored := record.Intent
	if stored.DeliveryID != intent.DeliveryID || stored.TenantID != intent.TenantID ||
		stored.RepositoryID != intent.RepositoryID || stored.AttemptID != intent.AttemptID ||
		stored.LeaseSetID != intent.LeaseSetID || stored.LeaseTTLMS != intent.LeaseTTLMS ||
		stored.PublicationRef != intent.PublicationRef ||
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
