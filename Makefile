.PHONY: build check-format smoke test test-integration test-race validate

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
	./scripts/smoke_durable_restart.sh

build:
	./scripts/check_reproducible_builds.sh

smoke:
	./scripts/smoke_kernel.sh

validate: check-format
	go mod verify
	go vet ./...
	go test ./...
	go test -race ./...
	./scripts/check_reproducible_builds.sh
	./scripts/smoke_kernel.sh
	python3 -m unittest discover -s tests -p 'test_*.py'
	python3 scripts/validate_repository.py
