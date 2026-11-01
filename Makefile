.DEFAULT_GOAL := check
# tools/download/main.go owns the versions, hashes and versioned binary paths.
TOOL_PATHS := $(shell go -C tools run ./download -paths)
ifneq ($(words $(TOOL_PATHS)),2)
$(error tools/download could not resolve the pinned binary paths)
endif
TAILWIND := $(word 1,$(TOOL_PATHS))
GOLANGCI_LINT := $(word 2,$(TOOL_PATHS))
TEMPL := ./bin/templ
CSS_ARGS := -i web/styles/app.css -o web/static/css/app.css --minify
# The shared PostgreSQL's max_connections=100 was sized from two concurrent
# checks at -p 2 (#636), so go test runs at most two packages at once by
# default; go test's own default (-p = CPUs) can exceed it. Override only for
# measurement, and re-measure before raising it (docs/worktree-env.md).
GO_TEST_FLAGS ?= -p 2
# CI passes GO_TEST_PACKAGES through tools/testshard; unset runs every package.

.PHONY: ai-env ai-env-clean ai-health db-health migrate seed check check-static check-test check-tools web-install check-web test-web prepush lint lint-fixtures vuln vuln-root vuln-tools vuln-tbls db-up db-down generate schema-docs deps api css dev bundles

bundles:
	go -C tools run ./webbuild .. "$$(git rev-parse HEAD)" "$${GITHUB_RUN_ID:-$$(date -u +%Y%m%dT%H%M%SZ)}" message-time

generate: bundles $(TEMPL)
	go generate ./internal/web/view
	$(TEMPL) generate
	go tool -modfile=tools/go.mod sqlc generate

schema-docs: bin/ai-db
	./bin/ai-db run go tool -modfile=tools/tbls/go.mod tbls doc --rm-dist

# Regenerates docs/dependencies.md; make check fails when it is stale.
deps: bundles
	go -C tools run ./deps ..

api: bundles
	go -C tools run ./apicheck ..

$(TEMPL): tools/go.mod tools/go.sum
	go -C tools build -o ../bin/templ github.com/a-h/templ/cmd/templ

css: $(TAILWIND)
	$(TAILWIND) $(CSS_ARGS)

# Serve rebuilt CSS from disk so updates do not depend on a server restart.
dev: bundles $(TEMPL) css bin/ai-db bin/dev
	@./bin/dev -tailwind $(TAILWIND) -templ $(TEMPL)

bin/dev: $(wildcard cmd/dev/*.go) go.mod go.sum
	go build -o $@ ./cmd/dev

$(TAILWIND):
	go -C tools run ./download tailwind ..

# tools/formatcheck skips root bin/ and node_modules/: third-party sources
# and invalid fixtures. Comparing formatter output keeps files untouched.
# The tools tests run with -count=1: scopecheck and
# tablecheck read db/queries, db/migrations and module_imports_test.go from
# outside the tools module, which Go's test cache does not track, so a cached
# pass would hide a changed query or manifest.
#
# check runs Go checks then TypeScript and client unit tests; CI splits these across jobs (#992),
# check-test once per tools/testshard shard, so the two cannot drift.
check:
	$(MAKE) check-static
	$(MAKE) check-test
	$(MAKE) check-tools
	$(MAKE) check-web
	$(MAKE) test-web

web-install:
	go -C tools run ./webtypes install ..

# Never installs; NOT VERIFIED unless pinned Node/checker exist, or fails with
# RIBBITTO_REQUIRE_NODE=1. CI web-types installs explicitly before checking.
check-web:
	go -C tools run ./webtypes check ..

test-web:
	go -C tools run ./webtypes test ..

check-static: bundles $(TEMPL)
	go -C tools run ./formatcheck -templ ../$(TEMPL) ..
	go vet ./...
	$(MAKE) lint
	$(MAKE) lint-fixtures
	go build ./...

check-test: bundles db-health
	go test -race $(GO_TEST_FLAGS) $(or $(GO_TEST_PACKAGES),./...)

check-tools: bundles $(TEMPL)
	go -C tools run ./languagecheck ..
	go -C tools vet ./apicheck
	go -C tools test -race ./apicheck
	go -C tools run ./apicheck -check ..
	go -C tools run ./domcontract -check ..
	go -C tools run ./contractliterals ..
	go -C tools run ./typedscripts ..
	go -C tools run ./deps --check ..
	go -C tools vet ./...
	go -C tools test -race -count=1 ./...
	go -C tools run ./testshard -check ..
	go -C tools run ./docscheck ..
	go -C tools run ./sourcecheck ..
	go -C tools run ./measurementskip ..

# A quick subset of make check to run before pushing a follow-up commit
# (#848): generated files and CSS are current, the API and dependency
# manifests and document sizes match, file kinds pass, and lint passes. Each
# check is its own recipe line, so make stops at the first non-zero status;
# nothing pipes a check's output, which once hid a docscheck failure. It does
# not run Go or templ formatting, the doc.go check, go vet, sourcecheck or
# the tests (make check does), nor the schema docs or make vuln (CI does).
prepush: bundles $(TEMPL) $(TAILWIND)
	$(MAKE) check-web
	go -C tools run ./languagecheck ..
	go -C tools run ./unchanged .. $(MAKE) generate css
	go -C tools run ./apicheck -check ..
	go -C tools run ./deps --check ..
	go -C tools run ./docscheck ..
	$(MAKE) lint

# Needs the Go vulnerability database over the network, so it runs in CI
# next to make check rather than inside it. Three scans, one per Go module, as
# separate targets: make -k runs all of them even after one fails, and a failed
# scan fails make vuln. Root and tools/ scan ./...; tools/tbls/ has no local
# packages, so it scans the named github.com/k1LoW/tbls main package.
vuln:
	$(MAKE) -k vuln-root vuln-tools vuln-tbls

vuln-root: bundles
	@printf '%s\n' 'Scanning root application module'
	go tool -modfile=tools/go.mod govulncheck ./...

vuln-tools:
	@printf '%s\n' 'Scanning tools/ module'
	go -C tools tool govulncheck ./...

vuln-tbls:
	@printf '%s\n' 'Scanning tools/tbls/ module (github.com/k1LoW/tbls)'
	go -C tools/tbls tool -modfile=../go.mod govulncheck github.com/k1LoW/tbls

# tools/lintfixtures proves the lint configuration rejects what it must, on
# the fixtures in internal/lintfixture; its package comment lists them.
lint-fixtures: $(GOLANGCI_LINT)
	go -C tools run ./lintfixtures -lint ../$(GOLANGCI_LINT) ..

lint: bundles $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run ./...

db-up:
	docker compose up -d --wait

db-down:
	docker compose down

$(GOLANGCI_LINT):
	go -C tools run ./download golangci-lint ..

# Credentials stay in the inherited admin environment, never in .env.local.
bin/ai-db: $(wildcard cmd/devdb/*.go) $(wildcard internal/platform/postgres/*.go) $(wildcard db/migrations/*.sql) go.mod go.sum
	mkdir -p "$(@D)"
	go build -o $@ ./cmd/devdb

ai-env: bin/ai-db
	@./bin/ai-db reserve

ai-env-clean: bin/ai-db
	@./bin/ai-db release

ai-health: bin/ai-db
	@./bin/ai-db health

db-health:
	@if [ "$${RIBBITTO_TEST_DATABASE_URL+x}" = x ] || [ "$${RIBBITTO_REQUIRE_DB:-}" = 1 ]; then $(MAKE) ai-health; fi

migrate: bundles bin/ai-db
	@./bin/ai-db run go run ./cmd/ribbitto migrate $(or $(DIRECTION),up)

seed: bundles bin/ai-db
	@./bin/ai-db run go run ./cmd/seed $(ARGS)

# Order-only: even an up-to-date helper must prepare embeds before use.
bin/dev bin/ai-db: | bundles
