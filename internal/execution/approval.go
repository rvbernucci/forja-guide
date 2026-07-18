package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rvbernucci/forja-guide/internal/contracts"
	"github.com/rvbernucci/forja-guide/internal/control"
	"github.com/rvbernucci/forja-guide/internal/fault"
	"github.com/rvbernucci/forja-guide/internal/persistence"
	"github.com/rvbernucci/forja-guide/internal/runstate"
)

const auditToolAuthorizeDelivery = "forja.authorize_delivery"

// ApprovalService records human approval of the complete delivery envelope.
// It is deliberately separate from the scheduler so scheduler authority cannot
// manufacture or widen approval.
type ApprovalService struct {
	repository persistence.DeliveryAuthorizationRepository
	registry   *contracts.Registry
}

// ApproveDeliveryInput carries one exact request and replay-safe command data.
type ApproveDeliveryInput struct {
	Request contracts.DeliveryRequest
	Command control.CommandContext
}

// NewApprovalService constructs the delivery approval boundary.
func NewApprovalService(
	repository persistence.DeliveryAuthorizationRepository,
) (*ApprovalService, error) {
	if repository == nil {
		return nil, fmt.Errorf("delivery authorization repository is required")
	}
	registry, err := contracts.NewRegistry()
	if err != nil {
		return nil, fmt.Errorf("compile delivery approval contracts: %w", err)
	}
	return &ApprovalService{repository: repository, registry: registry}, nil
}

// Approve persists a byte-exact authorization only for an authenticated human
// with decision authority who is independent from author and validator.
func (s *ApprovalService) Approve(
	ctx context.Context,
	principal control.Principal,
	input ApproveDeliveryInput,
) (persistence.DeliveryAuthorization, error) {
	if ctx == nil {
		return persistence.DeliveryAuthorization{}, fmt.Errorf("approval context is required")
	}
	if principal.ActorType != "human" {
		return persistence.DeliveryAuthorization{}, fault.New(
			fault.CodePermissionDenied,
			"execution.ApprovalService.Approve",
			"delivery authorization requires a human principal",
		)
	}
	if _, allowed := principal.Permissions[control.PermissionDecide]; !allowed {
		return persistence.DeliveryAuthorization{}, fault.New(
			fault.CodePermissionDenied,
			"execution.ApprovalService.Approve",
			"principal lacks decision authority",
		)
	}
	request := input.Request
	storageTenant, storageRepository, err := contracts.RepositoryStorageIdentity(
		request.TenantID, request.RepositoryID,
	)
	if err != nil {
		return persistence.DeliveryAuthorization{}, err
	}
	if principal.TenantID != storageTenant || principal.RepositoryID != storageRepository {
		return persistence.DeliveryAuthorization{}, fault.New(
			fault.CodePermissionDenied,
			"execution.ApprovalService.Approve",
			"principal authority does not match the delivery repository",
		)
	}
	if principal.ActorID == request.AuthorID || principal.ActorID == request.ValidatorID {
		return persistence.DeliveryAuthorization{}, fault.New(
			fault.CodePermissionDenied,
			"execution.ApprovalService.Approve",
			"delivery approver must be independent from author and validator",
		)
	}
	if err := validateDeliveryRequestDocument(s.registry, request); err != nil {
		return persistence.DeliveryAuthorization{}, err
	}
	metadata := runstate.CommandMetadata{
		IdempotencyKey: strings.TrimSpace(input.Command.IdempotencyKey),
		ActorType:      principal.ActorType,
		ActorID:        principal.ActorID,
		CorrelationID:  strings.TrimSpace(input.Command.CorrelationID),
		CausationID:    input.Command.CausationID,
		AuditToolName:  auditToolAuthorizeDelivery,
	}
	if err := runstate.ValidateCommandMetadata(metadata); err != nil {
		return persistence.DeliveryAuthorization{}, err
	}
	return s.repository.AuthorizeDelivery(ctx, request, metadata)
}

func validateDeliveryRequestDocument(
	registry *contracts.Registry,
	request contracts.DeliveryRequest,
) error {
	encoded, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode delivery request: %w", err)
	}
	if err := registry.ValidateJSON("delivery-request.schema.json", encoded); err != nil {
		return fmt.Errorf("validate delivery request schema: %w", err)
	}
	if err := contracts.ValidateDeliveryRequest(request); err != nil {
		return fmt.Errorf("validate delivery request semantics: %w", err)
	}
	return nil
}

func deliveryRequestDigest(
	request contracts.DeliveryRequest,
) (string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encode delivery request digest: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func requireExactAuthorization(
	request contracts.DeliveryRequest,
	authorization persistence.DeliveryAuthorization,
) error {
	want, err := deliveryRequestDigest(request)
	if err != nil {
		return err
	}
	approved, err := deliveryRequestDigest(authorization.Request)
	if err != nil {
		return err
	}
	if authorization.RequestSHA256 != want || approved != want ||
		authorization.ApprovedBy == "" || authorization.ApprovedAt.IsZero() ||
		authorization.ApprovedBy == request.AuthorID ||
		authorization.ApprovedBy == request.ValidatorID {
		return fmt.Errorf("delivery request differs from immutable human approval")
	}
	return nil
}
