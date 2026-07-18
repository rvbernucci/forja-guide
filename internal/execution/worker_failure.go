package execution

import (
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

// canonicalInfrastructureFailure converts a supervisor boundary failure into
// a secret-free terminal attempt record. The original error remains in the
// returned error chain, not in durable worker output.
func canonicalInfrastructureFailure(
	request contracts.DeliveryRequest,
	reason string,
) contracts.WorkerResult {
	now := time.Now().UTC()
	empty := sha256.Sum256(nil)
	return contracts.WorkerResult{
		TaskID:            request.TaskID,
		AttemptID:         request.AttemptID,
		RunID:             request.RunID,
		SchemaVersion:     workerTaskSchemaVersion,
		Adapter:           "execution-pipeline",
		Status:            "failed_retryable",
		Retryable:         true,
		TerminationReason: reason,
		StartedAt:         now,
		FinishedAt:        now,
		DurationMS:        0,
		Stdout:            "",
		Stderr:            "",
		StdoutSHA256:      fmt.Sprintf("%x", empty),
		StderrSHA256:      fmt.Sprintf("%x", empty),
		Usage:             contracts.WorkerUsage{},
		EvidenceRefs:      []string{},
	}
}
