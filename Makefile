GOLANGCI_LINT_VERSION := v2.14.0
.DEFAULT_GOAL := check
GOLANGCI_LINT := ./bin/golangci-lint-$(GOLANGCI_LINT_VERSION)/golangci-lint
TEMPL := ./bin/templ
TAILWIND_VERSION := v4.3.3
TAILWIND := ./bin/tailwindcss-$(TAILWIND_VERSION)
TAILWIND_SHA_macos-arm64 := cdf646702987a743464dff4d9c60fd4480d1c1e73dd819a9a67f1078815dce9d
TAILWIND_SHA_linux-x64 := dc61b3ac6b8c9ca874c0cc4c57b2409791a64c5540404ca5f5367360babc313a
CSS_ARGS := -i web/styles/app.css -o web/static/css/app.css --minify

.PHONY: check lint vuln db-up db-down generate schema-docs deps css dev

generate: $(TEMPL)
	$(TEMPL) generate
	go tool -modfile=tools/go.mod sqlc generate

schema-docs:
	go tool -modfile=tools/tbls/go.mod tbls doc --rm-dist

# Regenerates docs/dependencies.md; make check fails when it is stale.
deps:
	bash scripts/deps.sh

$(TEMPL): tools/go.mod tools/go.sum
	go -C tools build -o ../bin/templ github.com/a-h/templ/cmd/templ

css: $(TAILWIND)
	$(TAILWIND) $(CSS_ARGS)

# Serve rebuilt CSS from disk so updates do not depend on a server restart.
dev: $(TEMPL) css
	@set -eu; \
	$(TAILWIND) $(CSS_ARGS) --watch=always & css_pid=$$!; \
	trap 'kill "$$css_pid" 2>/dev/null || true; wait "$$css_pid" 2>/dev/null || true' EXIT; \
	trap 'exit 130' INT; trap 'exit 143' TERM; \
	RIBBITTO_DEV_ASSETS=web/static $(TEMPL) generate --watch --cmd 'go run ./cmd/ribbitto'

$(TAILWIND):
	@mkdir -p "$(@D)"
	@set -eu; platform="$$(uname -s)-$$(uname -m)"; \
	case "$$platform" in \
	  Darwin-arm64) asset=macos-arm64; sha=$(TAILWIND_SHA_macos-arm64) ;; \
	  Linux-x86_64) asset=linux-x64; sha=$(TAILWIND_SHA_linux-x64) ;; \
	  *) echo "Unsupported Tailwind platform: $$platform" >&2; exit 1 ;; \
	esac; \
	tmp=$$(mktemp "$@.XXXXXX"); trap 'rm -f "$$tmp"' EXIT; \
	curl -fsSL "https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/tailwindcss-$$asset" -o "$$tmp"; \
	printf '%s  %s\n' "$$sha" "$$tmp" | shasum -a 256 -c -; \
	chmod +x "$$tmp"; mv "$$tmp" "$@"

# Local caches under bin/ include third-party sources and invalid test fixtures.
# The pinned templ has no check-only flag, so each file is compared with the
# formatter's stdout; the working tree is never rewritten. The exit status is
# checked on its own: on a parse error the formatter prints nothing, which
# would match an empty file.
check: $(TEMPL)
	@set -eu; unformatted=$$(find . -path ./bin -prune -o -type f -name '*.go' -exec gofmt -l {} +); \
	if [ -n "$$unformatted" ]; then \
		echo "These files need gofmt:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	@set -eu; formatted=$$(mktemp); trap 'rm -f "$$formatted"' EXIT; \
	unformatted=$$(find . -path ./bin -prune -o -type f -name '*.templ' -print | sort | while IFS= read -r file; do \
			if ! $(TEMPL) fmt -stdout -stdin-filepath "$$file" < "$$file" > "$$formatted" \
				|| ! cmp -s "$$formatted" "$$file"; then printf '%s\n' "$$file"; fi; \
		done); \
	if [ -n "$$unformatted" ]; then \
		echo "These files need templ fmt (run ./bin/templ fmt on them):"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	@set -eu; missing=$$(find internal -type f -name '*.go' ! -name '*_test.go' ! -path '*/testdata/*' \
		| sed 's|/[^/]*$$||' | sort -u | while IFS= read -r dir; do \
			if [ ! -f "$$dir/doc.go" ]; then printf '%s\n' "$$dir"; fi; \
		done); \
	if [ -n "$$missing" ]; then \
		echo "These internal package directories are missing doc.go:"; \
		echo "$$missing"; \
		exit 1; \
	fi
	go vet ./...
	$(MAKE) lint
	go build ./...
	go test -race ./...
	bash scripts/deps.sh --check
	bash scripts/deps_test.sh
	go -C tools vet ./docscheck
	go -C tools test -race ./docscheck
	go -C tools run ./docscheck ..
	bash scripts/ai/grok-review_test.sh

# Needs the Go vulnerability database over the network, so it runs in CI
# next to make check rather than inside it. Scans the application module.
vuln:
	go tool -modfile=tools/go.mod govulncheck ./...

lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run ./...

db-up:
	docker compose up -d --wait

db-down:
	docker compose down

# Keep versions in separate directories so changing the pin fetches a new binary.
$(GOLANGCI_LINT):
	@mkdir -p "$(@D)"
	@set -eu; installer=$$(mktemp); \
	trap 'rm -f "$$installer"' EXIT; \
	curl -fsSL https://raw.githubusercontent.com/golangci/golangci-lint/$(GOLANGCI_LINT_VERSION)/install.sh -o "$$installer"; \
	sh "$$installer" -b "$(@D)" $(GOLANGCI_LINT_VERSION)
