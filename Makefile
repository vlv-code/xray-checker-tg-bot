# Local development gates — parity with .github/workflows/test.yml (CI).
# Zero external dependencies: plain make + the Go toolchain.
# `race` requires CGO with a C toolchain; on Windows see CONTRIBUTING.md
# ("Running the race detector locally").

.PHONY: gates race fmt cover

# Full non-race gate (build + vet + tests); each step aborts the target on failure.
gates:
	go build ./...
	go vet ./...
	go test -p 2 -count=1 -timeout 5m ./...

# Race detector (needs CGO + gcc; on Windows see CONTRIBUTING.md).
race:
	CGO_ENABLED=1 go test -race -timeout 5m ./...

# Format all Go files in place.
fmt:
	gofmt -w .

# Coverage profile + per-function summary (total coverage is the last line).
cover:
	go test -coverprofile=coverage.out -p 2 ./...
	go tool cover -func=coverage.out
