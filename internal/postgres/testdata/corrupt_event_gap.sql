INSERT INTO forja.events (
    event_id,
    tenant_id,
    repository_id,
    aggregate_type,
    aggregate_id,
    aggregate_version,
    event_type,
    schema_version,
    occurred_at,
    actor_type,
    actor_id,
    correlation_id,
    idempotency_key,
    payload
) VALUES (
    'event_00000000-0000-4000-8000-000000000098',
    '00000000-0000-4000-8000-000000000001',
    '00000000-0000-4000-8000-000000000002',
    'run',
    'run_00000000-0000-4000-8000-000000000099',
    2,
    'run.transitioned',
    '1.0',
    clock_timestamp(),
    'system',
    'corruption-fixture',
    'corruption-fixture',
    'corruption-fixture',
    '{
      "run_id": "run_00000000-0000-4000-8000-000000000099",
      "objective": "corrupt event stream",
      "state": "running",
      "version": 2,
      "updated_at": "2026-01-01T00:00:00Z"
    }'::jsonb
);
