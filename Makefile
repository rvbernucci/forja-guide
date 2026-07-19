.PHONY: adapters build check-format smoke smoke-mcp smoke-worker test test-integration test-race validate

adapters:
	npm ci --ignore-scripts --no-audit --no-fund

check-format:
	@test -z "$$(gofmt -l cmd internal schemas)" || \
		(echo "Go files require gofmt:"; gofmt -l cmd internal schemas; exit 1)

test:
	go test ./...

test-race:
	go test -race ./...

test-integration:
	@test -n "$$FORJA_TEST_DATABASE_URL" || \
		(echo "FORJA_TEST_DATABASE_URL is required"; exit 2)
	FORJA_TEST_BACKUP_RESTORE=1 go test -count=1 ./internal/postgres
	FORJA_TEST_DATABASE_URL="$$FORJA_TEST_DATABASE_URL" \
		go test -count=1 ./internal/observability -run '^TestOperationalSnapshot'
	FORJA_TEST_DELIVERY_DATABASE_URL="$$FORJA_TEST_DATABASE_URL" \
		go test -count=1 ./internal/delivery -run 'TestPublicationPostgres(EndToEnd|RecoversCrashAfterGitCAS)$$'
	FORJA_TEST_DELIVERY_DATABASE_URL="$$FORJA_TEST_DATABASE_URL" \
		go test -count=1 ./internal/execution \
		-run '^Test(PipelineApprovedSprintRunsRealWorkerAndPublishes|DeliveryAuthorizationSupportsNewAttemptForSameDelivery)$$'
	./scripts/rehearse_sprint08_indexing.sh
	./scripts/rehearse_sprint08_rollback.sh
	./scripts/rehearse_sprint07_rollback.sh
	./scripts/rehearse_sprint05_rollback.sh
	./scripts/smoke_durable_restart.sh

build:
	./scripts/check_reproducible_builds.sh

smoke:
	./scripts/smoke_kernel.sh

smoke-mcp:
	go test -count=1 ./internal/mcpserver -run 'TestMCPGovernedLifecycleAndAudit|TestToolCompatibilityFixture'

smoke-worker:
	go test -count=1 ./cmd/forja-worker -run TestRunExecutesOneShotWorker

validate: adapters check-format
	go mod verify
	go vet ./...
	go test ./...
	go test -race ./...
	./scripts/check_reproducible_builds.sh
	./scripts/smoke_kernel.sh
	$(MAKE) smoke-mcp
	$(MAKE) smoke-worker
	python3 -m unittest discover -s tests -p 'test_*.py'
	python3 scripts/validate_repository.py
