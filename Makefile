.PHONY: build check-format smoke test test-race validate

check-format:
	@test -z "$$(gofmt -l cmd internal schemas)" || \
		(echo "Go files require gofmt:"; gofmt -l cmd internal schemas; exit 1)

test:
	go test ./...

test-race:
	go test -race ./...

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
