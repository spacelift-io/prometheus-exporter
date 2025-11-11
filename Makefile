.PHONY: help build test clean snapshot release check-goreleaser

# Default target
help:
	@echo "Available targets:"
	@echo "  build           - Build the binary locally using go build"
	@echo "  test            - Run all tests"
	@echo "  snapshot        - Build a local snapshot release with goreleaser (no publish)"
	@echo "  release         - Create a release with goreleaser (requires tag)"
	@echo "  check-goreleaser - Check if goreleaser config is valid"
	@echo "  clean           - Remove build artifacts"

# Build the binary locally
build:
	go build -o spacelift-promex

# Run tests
test:
	go test -v ./...

# Build a snapshot release locally (no git tag required, won't publish)
snapshot:
	goreleaser release --snapshot --clean

# Build a full release (requires a git tag)
release:
	goreleaser release --clean

# Check if the goreleaser configuration is valid
check-goreleaser:
	goreleaser check

# Clean build artifacts
clean:
	rm -f spacelift-promex
	rm -rf dist/
