package postgres

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSprint05RollbackRunsSprint04BinaryAgainstDowngradedSchema(t *testing.T) {
	binary := os.Getenv("FORJA_TEST_SPRINT04_BINARY")
	if binary == "" {
		t.Skip("FORJA_TEST_SPRINT04_BINARY is not set")
	}
	if info, err := os.Stat(binary); err != nil || info.IsDir() {
		t.Fatalf("Sprint 04 binary is unavailable: info=%v err=%v", info, err)
	}
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatalf("migrate current schema before rollback: %v", err)
	}
	for _, version := range []int{6, 5, 4} {
		if err := RollbackLast(t.Context(), pool); err != nil {
			t.Fatalf("rollback migration %03d: %v", version, err)
		}
	}
	var migrationCount int
	if err := pool.QueryRow(
		t.Context(), "SELECT count(*) FROM forja.schema_migrations",
	).Scan(&migrationCount); err != nil {
		t.Fatalf("count downgraded migrations: %v", err)
	}
	if migrationCount != 3 {
		t.Fatalf("downgraded migration count = %d, want 3", migrationCount)
	}
	schemaDowngraded := true
	t.Cleanup(func() {
		if !schemaDowngraded {
			return
		}
		cleanupContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := Migrate(cleanupContext, pool); err != nil {
			t.Errorf("restore current schema after failed rollback rehearsal: %v", err)
		}
	})

	stdout, err := os.CreateTemp(t.TempDir(), "sprint04-stdout-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	stderr, err := os.CreateTemp(t.TempDir(), "sprint04-stderr-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	command := exec.Command(
		binary,
		"--listen", "127.0.0.1:0",
		"--environment", "rollback-rehearsal",
		"--shutdown-timeout", "2s",
		"--database-auto-migrate=false",
	)
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = append(
		os.Environ(),
		"FORJA_DATABASE_URL="+integrationDatabaseURL(t),
		"FORJA_HTTP_BEARER_TOKEN=forja-sprint04-rollback-rehearsal-token",
		"FORJA_HTTP_ACTOR_TYPE=system",
		"FORJA_HTTP_ACTOR_ID=sprint04-rollback-rehearsal",
	)
	if err := command.Start(); err != nil {
		t.Fatalf("start Sprint 04 binary: %v", err)
	}
	processDone := make(chan struct{})
	var processErr error
	go func() {
		processErr = command.Wait()
		close(processDone)
	}()
	stopped := false
	t.Cleanup(func() {
		if stopped || command.Process == nil {
			return
		}
		_ = command.Process.Signal(syscall.SIGTERM)
		select {
		case <-processDone:
		case <-time.After(3 * time.Second):
			_ = command.Process.Kill()
			<-processDone
		}
	})

	endpoint := waitForSprint04Readiness(
		t, stdout.Name(), stderr.Name(), processDone, &processErr,
	)
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(endpoint + "/readyz") // #nosec G107 -- parsed loopback test endpoint.
	if err != nil {
		t.Fatalf("probe Sprint 04 rollback daemon: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Sprint 04 readiness status = %d", response.StatusCode)
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("stop Sprint 04 rollback daemon: %v", err)
	}
	select {
	case <-processDone:
		stopped = true
		if processErr != nil {
			t.Fatalf("Sprint 04 rollback daemon exit: %v", processErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Sprint 04 rollback daemon did not stop within its bounded deadline")
	}
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatalf("reapply current schema after rollback rehearsal: %v", err)
	}
	schemaDowngraded = false
}

func TestSprint05RollbackRefusesPersistedDeliveryAuthorization(t *testing.T) {
	for _, test := range []struct {
		name   string
		insert string
	}{
		{
			name: "authorization event",
			insert: `
				WITH inserted AS (
					INSERT INTO forja.events (
						event_id, tenant_id, repository_id, aggregate_type,
						aggregate_id, aggregate_version, event_type, schema_version,
						occurred_at, actor_type, actor_id, correlation_id,
						idempotency_key, payload
					) VALUES (
						'event_rollback_delivery_authorized', $1, $2, 'approval',
						'delivery_00000000-0000-4000-8000-000000000003:attempt_00000000-0000-4000-8000-000000000004',
						1, 'delivery.authorized', '1.0', clock_timestamp(),
						'human', 'rollback-approver', 'rollback-authorization',
						'rollback-authorization', '{}'::jsonb
					)
					RETURNING event_id, tenant_id, repository_id
				)
				INSERT INTO forja.outbox (event_id, tenant_id, repository_id)
				SELECT event_id, tenant_id, repository_id FROM inserted`,
		},
		{
			name: "authorization receipt",
			insert: `
				INSERT INTO forja.idempotency_keys (
					tenant_id, scope, idempotency_key, request_hash,
					response_status, response_body
				) VALUES (
					$1,
					'authorize_delivery:' || $2::text || ':delivery_00000000-0000-4000-8000-000000000003:attempt_00000000-0000-4000-8000-000000000004',
					'rollback-authorization', decode(repeat('00', 32), 'hex'),
					200, '{}'::jsonb
				)`,
		},
		{
			name: "queued blocked-run resume history",
			insert: `
				WITH inserted AS (
					INSERT INTO forja.events (
						event_id, tenant_id, repository_id, aggregate_type,
						aggregate_id, aggregate_version, event_type, schema_version,
						occurred_at, actor_type, actor_id, correlation_id,
						idempotency_key, payload
					) VALUES
						(
							'event_rollback_blocked_resume_1', $1, $2, 'run',
							'run_00000000-0000-4000-8000-000000000005', 1,
							'run.transitioned', '1.0', clock_timestamp(),
							'system', 'rollback-scheduler', 'rollback-resume',
							'rollback-resume-awaiting', '{"state":"awaiting_decision"}'::jsonb
						),
						(
							'event_rollback_blocked_resume_2', $1, $2, 'run',
							'run_00000000-0000-4000-8000-000000000005', 2,
							'run.transitioned', '1.0', clock_timestamp(),
							'system', 'rollback-scheduler', 'rollback-resume',
							'rollback-resume-queued', '{"state":"queued"}'::jsonb
						)
					RETURNING event_id, tenant_id, repository_id, aggregate_version
				)
				INSERT INTO forja.outbox (event_id, tenant_id, repository_id)
				SELECT event_id, tenant_id, repository_id
				FROM inserted
				ORDER BY aggregate_version`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool := integrationPool(t)
			resetDatabase(t, pool)
			if err := Migrate(t.Context(), pool); err != nil {
				t.Fatalf("migrate current schema: %v", err)
			}
			if _, err := pool.Exec(
				t.Context(), test.insert, DefaultTenantID, DefaultRepositoryID,
			); err != nil {
				t.Fatalf("persist Sprint 05 authorization evidence: %v", err)
			}
			if err := RollbackLast(t.Context(), pool); err == nil ||
				!strings.Contains(err.Error(), "incompatible with the Sprint 04 verifier") {
				t.Fatalf("rollback error = %v, want Sprint 04 compatibility refusal", err)
			}
			var migrationCount int
			if err := pool.QueryRow(
				t.Context(), "SELECT count(*) FROM forja.schema_migrations",
			).Scan(&migrationCount); err != nil {
				t.Fatal(err)
			}
			if migrationCount != 6 {
				t.Fatalf("failed rollback changed migration count to %d", migrationCount)
			}
		})
	}
}

func TestSprint05RollbackRefusesConcurrentCommandWriter(t *testing.T) {
	pool := integrationPool(t)
	resetDatabase(t, pool)
	if err := Migrate(t.Context(), pool); err != nil {
		t.Fatalf("migrate current schema: %v", err)
	}
	writer, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback(t.Context()) }()
	if _, err := writer.Exec(
		t.Context(), "LOCK TABLE forja.idempotency_keys IN ROW EXCLUSIVE MODE",
	); err != nil {
		t.Fatalf("hold command-writer relation lock: %v", err)
	}

	if err := RollbackLast(t.Context(), pool); err == nil ||
		!strings.Contains(err.Error(), "lock incremental migration writers") {
		t.Fatalf("rollback error = %v, want concurrent-writer refusal", err)
	}
	var migrationCount int
	if err := pool.QueryRow(
		t.Context(), "SELECT count(*) FROM forja.schema_migrations",
	).Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != 6 {
		t.Fatalf("refused rollback changed migration count to %d", migrationCount)
	}
}

func waitForSprint04Readiness(
	t *testing.T,
	stdoutPath string,
	stderrPath string,
	processDone <-chan struct{},
	processErr *error,
) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-processDone:
			stderr, _ := os.ReadFile(stderrPath)
			t.Fatalf("Sprint 04 binary exited before readiness: %v: %s", *processErr, stderr)
		default:
		}
		data, _ := os.ReadFile(stdoutPath)
		for _, line := range strings.Split(string(data), "\n") {
			var record struct {
				Message string `json:"msg"`
				Listen  string `json:"listen"`
			}
			if json.Unmarshal([]byte(line), &record) == nil &&
				record.Message == "forjad ready" &&
				strings.HasPrefix(record.Listen, "127.0.0.1:") {
				return "http://" + record.Listen
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	stderr, _ := os.ReadFile(stderrPath)
	t.Fatalf("Sprint 04 binary did not become ready: %s", stderr)
	return ""
}
