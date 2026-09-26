GOLANGCI_LINT_VERSION := v2.14.0
.DEFAULT_GOAL := check
GOLANGCI_LINT := ./bin/golangci-lint-$(GOLANGCI_LINT_VERSION)/golangci-lint
TEMPL := ./bin/templ
TAILWIND_VERSION := v4.1.13
TAILWIND := ./bin/tailwindcss-$(TAILWIND_VERSION)
TAILWIND_SHA_macos-arm64 := c47681e9948db20026a913a4aca4ee0269b4c0d4ef3f71343cb891dfdc1e97c9
TAILWIND_SHA_linux-x64 := b9ed9f8f640d3323711f9f68608aa266dff3adbc42e867c38ea2d009b973be11
CSS_ARGS := -i web/styles/app.css -o web/static/css/app.css --minify

.PHONY: check lint db-up db-down generate schema-docs css dev

generate: $(TEMPL)
	$(TEMPL) generate
	go tool -modfile=tools/go.mod sqlc generate

schema-docs:
	go tool -modfile=tools/go.mod tbls doc --rm-dist

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
check:
	@set -eu; unformatted=$$(find . -path ./bin -prune -o -type f -name '*.go' -exec gofmt -l {} +); \
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
