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
# The AI launchers' self-tests take most of check's time, so check-launchers
# runs them only when the change since this base can affect them (an empty base always
# runs them); scripts/ai/launcher-tests.sh says what it chose and why. CI
# passes HEAD^1 on a pull request (its merge commit's first parent, the base
# as merged), or an empty base on main and nightly. It is exported so the
# recipe reads it from the environment, unquoted by make.
LAUNCHER_TESTS_BASE ?= origin/main
export LAUNCHER_TESTS_BASE
# The shared PostgreSQL's max_connections=100 was sized from two concurrent
# checks at -p 2 (#636), so go test runs at most two packages at once by
# default; go test's own default (-p = CPUs) can exceed it. Override only for
# measurement, and re-measure before raising it (docs/worktree-env.md).
GO_TEST_FLAGS ?= -p 2
# CI's test jobs each pass one shard from tools/testshard; locally every
# package runs.
GO_TEST_PACKAGES ?= ./...

.PHONY: ai-env ai-env-clean ai-health db-health migrate seed check-ai-env check check-static check-test check-tools check-launchers check-ai prepush lint lint-fixtures vuln db-up db-down generate schema-docs deps api css dev

generate: $(TEMPL)
	$(TEMPL) generate
	go tool -modfile=tools/go.mod sqlc generate

schema-docs:
	python3 scripts/ai_env.py run go tool -modfile=tools/tbls/go.mod tbls doc --rm-dist

# Regenerates docs/dependencies.md; make check fails when it is stale.
deps:
	go -C tools run ./deps ..

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
	RIBBITTO_DEV_ASSETS=web/static python3 scripts/ai_env.py run $(TEMPL) generate --watch --cmd 'go run ./cmd/ribbitto'

$(TAILWIND):
	go -C tools run ./download tailwind ..

# tools/formatcheck excludes caches under bin/: they contain third-party
# sources and invalid fixtures. Comparing formatter output keeps files untouched.
# The tools tests run with -count=1: scopecheck and
# tablecheck read db/queries, db/migrations and module_imports_test.go from
# outside the tools module, which Go's test cache does not track, so a cached
# pass would hide a changed query or manifest.
#
# check runs its four parts in order; CI runs them in parallel jobs (#992),
# check-test once per tools/testshard shard, so the two cannot drift.
check:
	$(MAKE) check-static
	$(MAKE) check-test
	$(MAKE) check-tools
	$(MAKE) check-launchers

check-static: $(TEMPL)
	go -C tools run ./formatcheck -templ ../$(TEMPL) ..
	go vet ./...
	$(MAKE) lint
	$(MAKE) lint-fixtures
	go build ./...

check-test: db-health
	go test -race $(GO_TEST_FLAGS) $(GO_TEST_PACKAGES)

check-tools: $(TEMPL)
	$(MAKE) check-ai-env
	go -C tools vet ./apicheck
	go -C tools test -race ./apicheck
	go -C tools run ./apicheck -check ..
	go -C tools run ./deps --check ..
	go -C tools vet ./...
	go -C tools test -race -count=1 ./...
	go -C tools run ./testshard -check ..
	go -C tools run ./docscheck ..
	go -C tools run ./sourcecheck ..

check-launchers:
	bash scripts/ai/launcher-tests_test.sh
	bash scripts/ai/launcher-tests.sh --base "$$LAUNCHER_TESTS_BASE"

# Always runs the AI launchers' self-tests, whatever changed.
check-ai:
	bash scripts/ai/launcher-tests.sh

# A quick subset of make check to run before pushing a follow-up commit
# (#848): generated files and CSS are current, the API and dependency
# manifests and document sizes match, and lint passes. Each check is its own
# recipe line, so make stops at the first non-zero status; nothing pipes a
# check's output, which once hid a docscheck failure. It does not run Go or
# templ formatting, the doc.go check, go vet, sourcecheck or the tests
# (make check does), nor the schema docs or make vuln (CI does).
prepush: $(TEMPL) $(TAILWIND)
	go -C tools run ./unchanged .. $(MAKE) generate css
	go -C tools run ./apicheck -check ..
	go -C tools run ./deps --check ..
	go -C tools run ./docscheck ..
	$(MAKE) lint

# Needs the Go vulnerability database over the network, so it runs in CI
# next to make check rather than inside it. Scans the application module.
vuln:
	go tool -modfile=tools/go.mod govulncheck ./...

# tools/lintfixtures proves the lint configuration rejects what it must, on
# the fixtures in internal/lintfixture; its package comment lists them.
lint-fixtures: $(GOLANGCI_LINT)
	go -C tools run ./lintfixtures -lint ../$(GOLANGCI_LINT) ..

lint: $(GOLANGCI_LINT)
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

ai-env ai-env-clean ai-health: bin/ai-db
	@python3 scripts/ai_env.py $(patsubst ai-env-clean,clean,$(patsubst ai-health,health,$@))

db-health:
	@if [ "$${RIBBITTO_TEST_DATABASE_URL+x}" = x ] || [ "$${RIBBITTO_REQUIRE_DB:-}" = 1 ]; then $(MAKE) ai-health; fi

migrate:
	@python3 scripts/ai_env.py run go run ./cmd/ribbitto migrate $(or $(DIRECTION),up)

seed:
	@python3 scripts/ai_env.py run go run ./cmd/seed $(ARGS)

check-ai-env: bin/ai-db
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts -p 'ai_*_test.py'
