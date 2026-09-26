GOLANGCI_LINT_VERSION := v2.14.0
GOLANGCI_LINT := ./bin/golangci-lint-$(GOLANGCI_LINT_VERSION)/golangci-lint

.PHONY: check lint

check:
	@set -eu; unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "These files need gofmt:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	go vet ./...
	$(MAKE) lint
	go build ./...
	go test ./...

lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run ./...

# Keep versions in separate directories so changing the pin fetches a new binary.
$(GOLANGCI_LINT):
	@mkdir -p "$(@D)"
	@set -eu; installer=$$(mktemp); \
	trap 'rm -f "$$installer"' EXIT; \
	curl -fsSL https://raw.githubusercontent.com/golangci/golangci-lint/$(GOLANGCI_LINT_VERSION)/install.sh -o "$$installer"; \
	sh "$$installer" -b "$(@D)" $(GOLANGCI_LINT_VERSION)
