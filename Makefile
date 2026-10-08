GOLANGCI_LINT_VERSION := v2.14.0
.DEFAULT_GOAL := check
GOLANGCI_LINT := ./bin/golangci-lint-$(GOLANGCI_LINT_VERSION)/golangci-lint
TEMPL := ./bin/templ
TAILWIND_VERSION := v4.3.3
TAILWIND := ./bin/tailwindcss-$(TAILWIND_VERSION)
TAILWIND_SHA_macos-arm64 := cdf646702987a743464dff4d9c60fd4480d1c1e73dd819a9a67f1078815dce9d
TAILWIND_SHA_linux-x64 := dc61b3ac6b8c9ca874c0cc4c57b2409791a64c5540404ca5f5367360babc313a
CSS_ARGS := -i web/styles/app.css -o web/static/css/app.css --minify
# The AI launchers' self-tests take most of check's time, so check runs them
# only when the change since this base can affect them (an empty base always
# runs them); scripts/ai/launcher-tests.sh says what it chose and why. CI
# passes HEAD^1 on a pull request (its merge commit's first parent, the base
# as merged), or an empty base on main and nightly. It is exported so the
# recipe reads it from the environment, unquoted by make.
LAUNCHER_TESTS_BASE ?= origin/main
export LAUNCHER_TESTS_BASE

.PHONY: check check-ai lint lint-fixtures vuln db-up db-down generate schema-docs deps api css dev

generate: $(TEMPL)
	$(TEMPL) generate
	go tool -modfile=tools/go.mod sqlc generate

schema-docs:
	go tool -modfile=tools/tbls/go.mod tbls doc --rm-dist

# Regenerates docs/dependencies.md; make check fails when it is stale.
deps:
	bash scripts/deps.sh

api:
	go -C tools run ./apicheck ..

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
	$(MAKE) lint-fixtures
	go build ./...
	go test -race ./...
	go -C tools vet ./apicheck
	go -C tools test -race ./apicheck
	go -C tools run ./apicheck -check ..
	bash scripts/deps.sh --check
	bash scripts/deps_test.sh
	go -C tools vet ./...
	go -C tools test -race ./...
	go -C tools run ./docscheck ..
	go -C tools run ./sourcecheck ..
	bash scripts/ai/launcher-tests_test.sh
	bash scripts/ai/launcher-tests.sh --base "$$LAUNCHER_TESTS_BASE"

# Always runs the AI launchers' self-tests, whatever changed.
check-ai:
	bash scripts/ai/launcher-tests.sh

# Needs the Go vulnerability database over the network, so it runs in CI
# next to make check rather than inside it. Scans the application module.
vuln:
	go tool -modfile=tools/go.mod govulncheck ./...

# The lint fixtures (internal/lintfixture, built only with the lintfixture
# tag) prove that depguard rejects a module root importing pgxbridge and
# accepts a store doing so, and staticcheck rejects ignored results even in
# generated templates, bodyclose rejects unclosed HTTP response bodies,
# sqlclosecheck rejects unused, unclosed pgx rows, and nilerr rejects returning
# nil after checking a non-nil error; plain lint never sees them. Any other rows
# use satisfies sqlclosecheck: keep defer rows.Close(); lint does not catch a
# missing Close once rows are used.
lint-fixtures: $(GOLANGCI_LINT)
	@set -eu; status=0; \
	out=$$($(GOLANGCI_LINT) run --build-tags lintfixture ./internal/lintfixture/... 2>&1) || status=$$?; \
	if [ "$$status" -eq 0 ] || ! printf '%s\n' "$$out" | grep -q 'lintfixture/badroot/root.go.*pgxbridge'; then \
		echo "lint-fixtures: depguard must reject pgxbridge in a module root"; printf '%s\n' "$$out"; exit 1; \
	fi; \
	if printf '%s\n' "$$out" | grep -q 'lintfixture/internal/postgres/'; then \
		echo "lint-fixtures: depguard must accept pgxbridge in a store"; printf '%s\n' "$$out"; exit 1; \
	fi; \
	for file in bad.go bad_templ.go; do \
		if ! printf '%s\n' "$$out" | grep -F "lintfixture/$$file:" | grep -q 'SA4017:.*TrimSpace'; then \
			echo "lint-fixtures: staticcheck must reject an ignored result in $$file"; printf '%s\n' "$$out"; exit 1; \
		fi; \
	done; \
	if ! printf '%s\n' "$$out" | grep -F 'lintfixture/bodyclose.go:' | grep -q 'response body must be closed.*(bodyclose)'; then \
		echo "lint-fixtures: bodyclose must reject an unclosed HTTP response body"; printf '%s\n' "$$out"; exit 1; \
	fi; \
	if ! printf '%s\n' "$$out" | grep -F 'lintfixture/sqlclosecheck.go:' | grep -Fq 'Rows/Stmt/NamedStmt was not closed (sqlclosecheck)'; then \
		echo "lint-fixtures: sqlclosecheck must reject unused, unclosed pgx rows"; printf '%s\n' "$$out"; exit 1; \
	fi; \
	if ! printf '%s\n' "$$out" | grep -F 'lintfixture/nilerr.go:' | grep -q 'error is not nil (line [0-9]*) but it returns nil (nilerr)'; then \
		echo "lint-fixtures: nilerr must reject returning nil after a non-nil error"; printf '%s\n' "$$out"; exit 1; \
	fi

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
