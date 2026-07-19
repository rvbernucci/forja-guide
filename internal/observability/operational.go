package observability

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
)

// OperationalSnapshot contains only aggregate, content-free runtime state.
type OperationalSnapshot struct {
	StuckRuns        int64
	ExpiredLeases    int64
	PendingOutbox    int64
	InflightOutbox   int64
	DeadOutbox       int64
	ProjectionLag    int64
	PendingApprovals int64
	WorkerCrashLoops int64
}

// OperationalReader obtains one bounded aggregate snapshot.
type OperationalReader interface {
	OperationalSnapshot(context.Context, OperationalThresholds) (OperationalSnapshot, error)
}

// OperationalThresholds defines bounded anomaly windows.
type OperationalThresholds struct {
	StuckAfter      time.Duration
	CrashLoopWindow time.Duration
	CrashLoopCount  int
	QueryTimeout    time.Duration
}

// DefaultOperationalThresholds returns conservative initial alert windows.
func DefaultOperationalThresholds() OperationalThresholds {
	return OperationalThresholds{
		StuckAfter:      15 * time.Minute,
		CrashLoopWindow: 15 * time.Minute,
		CrashLoopCount:  3,
		QueryTimeout:    2 * time.Second,
	}
}

func (thresholds OperationalThresholds) validate() error {
	if thresholds.StuckAfter <= 0 || thresholds.StuckAfter > 24*time.Hour ||
		thresholds.CrashLoopWindow <= 0 || thresholds.CrashLoopWindow > 24*time.Hour ||
		thresholds.CrashLoopCount < 2 || thresholds.CrashLoopCount > 100 ||
		thresholds.QueryTimeout <= 0 || thresholds.QueryTimeout > 30*time.Second {
		return fmt.Errorf("operational thresholds are outside bounded limits")
	}
	return nil
}

type queryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// PostgresOperationalReader derives non-authoritative state from PostgreSQL.
type PostgresOperationalReader struct {
	queryer      queryRower
	tenantID     string
	repositoryID string
}

// NewPostgresOperationalReader binds aggregate reads to one authority.
func NewPostgresOperationalReader(
	queryer queryRower,
	tenantID string,
	repositoryID string,
) (*PostgresOperationalReader, error) {
	if queryer == nil || tenantID == "" || repositoryID == "" {
		return nil, fmt.Errorf("operational reader requires a queryer and authority")
	}
	return &PostgresOperationalReader{
		queryer: queryer, tenantID: tenantID, repositoryID: repositoryID,
	}, nil
}

// OperationalSnapshot executes one read-only aggregate statement. It never
// selects task content, objectives, raw errors, or identifying columns.
func (reader *PostgresOperationalReader) OperationalSnapshot(
	ctx context.Context,
	thresholds OperationalThresholds,
) (OperationalSnapshot, error) {
	var snapshot OperationalSnapshot
	err := reader.queryer.QueryRow(ctx, `
		WITH authority_outbox AS (
			SELECT state, outbox_id
			FROM forja.outbox
			WHERE tenant_id=$1 AND repository_id=$2
		), crash_loops AS (
			SELECT attempt.run_id
			FROM forja.attempts AS attempt
			JOIN forja.runs AS run
			  ON run.tenant_id=attempt.tenant_id AND run.run_id=attempt.run_id
			WHERE attempt.tenant_id=$1 AND run.repository_id=$2
			  AND attempt.status='failed_retryable'
			  AND attempt.updated_at >= clock_timestamp() - make_interval(secs => $4)
			GROUP BY attempt.run_id
			HAVING count(*) >= $5
		)
		SELECT
			(SELECT count(*) FROM forja.runs AS candidate
			 WHERE candidate.tenant_id=$1 AND candidate.repository_id=$2
			   AND candidate.state IN ('queued', 'preparing', 'running', 'validating', 'cancelling')
			   AND candidate.updated_at <= clock_timestamp() - make_interval(secs => $3)
			   AND NOT EXISTS (
				SELECT 1
				FROM forja.attempts AS attempt
				JOIN forja.leases AS live_lease
				  ON live_lease.tenant_id=attempt.tenant_id
				 AND live_lease.repository_id=candidate.repository_id
				 AND live_lease.resource_type=attempt.lease_resource_type
				 AND live_lease.resource_id=attempt.lease_resource_id
				 AND live_lease.owner_id=attempt.worker_id
				 AND live_lease.fencing_token=attempt.fencing_token
				WHERE attempt.tenant_id=candidate.tenant_id
				  AND attempt.run_id=candidate.run_id
				  AND attempt.status IN ('queued', 'running', 'succeeded')
				  AND live_lease.expires_at > clock_timestamp()
			   )),
			(SELECT count(*) FROM forja.leases
			 WHERE tenant_id=$1 AND repository_id=$2
			   AND expires_at <= clock_timestamp()
			   AND expires_at > updated_at
			   AND NOT EXISTS (
				SELECT 1
				FROM forja.lease_set_members AS member
				JOIN forja.lease_sets AS lease_set
				  ON lease_set.tenant_id=member.tenant_id
				 AND lease_set.repository_id=member.repository_id
				 AND lease_set.lease_set_id=member.lease_set_id
				WHERE member.tenant_id=$1 AND member.repository_id=$2
				  AND member.resource_type=leases.resource_type
				  AND member.resource_id=leases.resource_id
				  AND member.fencing_token=leases.fencing_token
				  AND lease_set.state='released'
			   )),
			(SELECT count(*) FROM authority_outbox WHERE state='pending'),
			(SELECT count(*) FROM authority_outbox WHERE state='inflight'),
			(SELECT count(*) FROM authority_outbox WHERE state='dead'),
			(SELECT count(*)
			 FROM authority_outbox
			 WHERE outbox_id > COALESCE(
				(SELECT min(last_outbox_id)
				 FROM forja.projection_checkpoints
				 WHERE tenant_id=$1 AND repository_id=$2),
				0
			 )),
			(SELECT count(*) FROM forja.decisions
			 WHERE tenant_id=$1 AND repository_id=$2 AND status='pending'),
			(SELECT count(*) FROM crash_loops)`,
		reader.tenantID,
		reader.repositoryID,
		int64(thresholds.StuckAfter/time.Second),
		int64(thresholds.CrashLoopWindow/time.Second),
		thresholds.CrashLoopCount,
	).Scan(
		&snapshot.StuckRuns,
		&snapshot.ExpiredLeases,
		&snapshot.PendingOutbox,
		&snapshot.InflightOutbox,
		&snapshot.DeadOutbox,
		&snapshot.ProjectionLag,
		&snapshot.PendingApprovals,
		&snapshot.WorkerCrashLoops,
	)
	return snapshot, err
}

// OperationalCollector exports aggregate state without caching authority.
type OperationalCollector struct {
	reader     OperationalReader
	thresholds OperationalThresholds
	condition  *prometheus.Desc
	success    *prometheus.Desc
	duration   *prometheus.Desc
}

// NewOperationalCollector creates one bounded custom collector.
func NewOperationalCollector(
	reader OperationalReader,
	thresholds OperationalThresholds,
) (*OperationalCollector, error) {
	if reader == nil {
		return nil, fmt.Errorf("operational reader is required")
	}
	if err := thresholds.validate(); err != nil {
		return nil, err
	}
	return &OperationalCollector{
		reader:     reader,
		thresholds: thresholds,
		condition: prometheus.NewDesc(
			"forja_operational_condition_items",
			"Current content-free item count for each bounded operational condition.",
			[]string{"condition"}, nil,
		),
		success: prometheus.NewDesc(
			"forja_operational_collection_success",
			"Whether the most recent operational state collection succeeded.",
			nil, nil,
		),
		duration: prometheus.NewDesc(
			"forja_operational_collection_duration_seconds",
			"Duration of the most recent operational state collection.",
			nil, nil,
		),
	}, nil
}

func (collector *OperationalCollector) Describe(channel chan<- *prometheus.Desc) {
	channel <- collector.condition
	channel <- collector.success
	channel <- collector.duration
}

func (collector *OperationalCollector) Collect(channel chan<- prometheus.Metric) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), collector.thresholds.QueryTimeout)
	defer cancel()
	snapshot, err := collector.reader.OperationalSnapshot(ctx, collector.thresholds)
	success := 1.0
	if err != nil {
		success = 0
	}
	channel <- prometheus.MustNewConstMetric(collector.success, prometheus.GaugeValue, success)
	channel <- prometheus.MustNewConstMetric(
		collector.duration, prometheus.GaugeValue, time.Since(started).Seconds(),
	)
	if err != nil {
		return
	}
	values := []struct {
		condition string
		value     int64
	}{
		{"stuck_runs", snapshot.StuckRuns},
		{"expired_leases", snapshot.ExpiredLeases},
		{"pending_outbox", snapshot.PendingOutbox},
		{"inflight_outbox", snapshot.InflightOutbox},
		{"dead_outbox", snapshot.DeadOutbox},
		{"projection_lag", snapshot.ProjectionLag},
		{"pending_approvals", snapshot.PendingApprovals},
		{"worker_crash_loops", snapshot.WorkerCrashLoops},
	}
	for _, item := range values {
		channel <- prometheus.MustNewConstMetric(
			collector.condition,
			prometheus.GaugeValue,
			float64(item.value),
			item.condition,
		)
	}
}
