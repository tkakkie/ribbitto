# AGENTS.md

Rules for AI coding tools and the people using them. Keep this file short;
explanations of the product and the code belong in `docs/`.

## Project

ribbitto is a self-hostable team chat: Go, `net/http`, templ, htmx with
Server-Sent Events, Tailwind CSS, PostgreSQL (pgx, sqlc, goose). One
maintainer, public repository. Everything in the repository — code,
comments, docs, commits, issues, pull requests — is written in English.

## Workflow

One set of rules binds every AI; see [Where things are explained](#where-things-are-explained).

## Commands

- `make check` — checks formatting (Go and templ), vets, lints, builds and
  tests (race detector on); must pass before you open a pull request.
  Format templates with `./bin/templ fmt <path>`.
- `make vuln` — runs `govulncheck`; needs the network, so CI runs it,
  not `make check`.
- `make lint` — runs golangci-lint.
- Tooling and orchestration are Go, with one-line `Makefile` recipes
  ([decision 35](docs/decisions/35-tooling-in-go.md)); the limited Node
  exception follows [decision 43](docs/decisions/43-typescript-with-a-go-driven-build.md).
- `make web-install` — installs the locked checker; `make check-web` checks
  TypeScript and `make test-web` runs the client unit tests (both in
  `make check`). Follow [the web toolchain](docs/architecture/web-toolchain.md#versions-and-installation--1410-1415).
- `make deps` — regenerates `docs/dependencies.md` (package-import
  edges); commit it with any edge change.
- `make check` also checks documents: links and `#anchors`, repository
  paths in code spans, documentation image sizes, destinations and alt text,
  and each document's size limit (one topic per file).
  When a file goes over, open an issue, add `<path> #<issue>` to
  `docs/size-exceptions.txt`, and split it in its own pull request.

## Layout and dependency direction

`cmd/ribbitto` and `cmd/seed` do the wiring, sharing adapters through
`internal/compose` (composition only; no business logic or service layer).
Only `cmd/*` and `_test.go` files import `compose`; it imports module roots,
their wiring, `kernel` and `platform`. These rules include compose sub-packages.
[`packages.md`](docs/architecture/packages.md) defines the import rules and
the module manifest test (`module_imports_test.go`) checks the module rules;
depguard keeps the non-module rules. Never break them.
Modules ([`modules.md`](docs/architecture/modules.md), from decision 26):

- Others import only a module's root (`internal/identity`, `internal/realtime`,
  `internal/org`, `internal/conversation`, `internal/presence`,
  `internal/typing`, `internal/unread`); its
  store only its wiring (`<module>pg`) and own tests; the wiring only
  `cmd/*`, `compose` and tests. Exception: a module's fixture package (`<module>test`,
  decision 29) is imported only by `_test.go` files and by the fixture
  packages above it (`conversationtest` → `orgtest` → `identitytest`);
  production code never imports one.
- `web` imports module roots and `kernel`. Authorization is `org`'s root.
- Use cases return plain structs; only `internal/web` produces HTML.
- Only `platform/postgres` imports `db/migrations`.

Features ([map](docs/architecture/features.md)):

- New code goes in its module.
- Read a package's `doc.go` before touching it.
- Use another feature only through its exported API, never its store
  internals or queries.
- Write only your feature's tables, except flows the feature map lists (a
  new one needs its issue to say why).

## Where things are explained

Read and update the relevant doc in the same PR:
[`docs/architecture/`](docs/architecture/README.md) (packages, imports, data flow, real-time),
[`docs/domain/README.md`](docs/domain/README.md) (terms, entities, invariants, unread),
[`DECISIONS.md`](DECISIONS.md) (`docs/decisions/` index — change via issue),
[`docs/database.md`](docs/database.md), [`docs/ui.md`](docs/ui.md),
[`docs/ui-colours.md`](docs/ui-colours.md) (token utilities only, never raw colours; `tools/sourcecheck`)
and [`docs/accessibility.md`](docs/accessibility.md).

Workflow reading: implementers read this file, the touched area's docs, and
action-triggered sections — [own token, credentials, `ready`, merge](docs/workflow/README.md#what-no-ai-does),
[tool failures and disposable DBs](docs/workflow/ai-credentials.md#tool-failures-and-disposable-databases),
[PR body credential scan](docs/workflow/delivery.md#running-a-review) and
[measurement caps](docs/workflow/investigations.md#investigations-and-the-performance-target).
Cross-reviewers read this file and [PR cross-review](docs/workflow/reviewing.md#pull-request-cross-review)
(or [issue review](docs/workflow/reviewing.md#issue-review)). The orchestrator
reads the [workflow README](docs/workflow/README.md) ([independence](docs/workflow/README.md#principles),
[ruleset](docs/workflow/README.md#what-github-enforces-and-what-is-only-a-rule),
[head SHA](docs/workflow/README.md#lifecycle)) and [`delivery.md`](docs/workflow/delivery.md)
([third-AI rules](docs/workflow/delivery.md#third-ai-review)). Read
[`ai-credentials.md`](docs/workflow/ai-credentials.md),
[`investigations.md`](docs/workflow/investigations.md) and `docs/decisions/` only
for their subject.

## Writing code

- Use only the [language list](docs/decisions/42-repository-languages-and-formats.md#language-list)
  and [decision 44](docs/decisions/44-remove-the-launcher-language-entries.md);
  if a task needs another, stop and ask the maintainer in the issue.
- Boring Go: no generics or reflection unless removing real duplication, no
  global state, pass `context.Context`, wrap errors (`fmt.Errorf("doing x: %w", err)`).
- Every `internal/` package has a `doc.go` stating its responsibility; module
  roots list owned tables, event kinds and reasons, not APIs. `cmd/...` uses the
  main file's package comment (no empty `doc.go`). Exported identifiers have doc
  comments.
- Comments explain **why** (non-obvious choice, constraint or trap), not what;
  do not narrate code.
- Organisation-owned data is always scoped by `organization_id` from the URL,
  never a request body; non-members get 404. Setup creates the organisation;
  installation-wide sign-up and `/` resolve it from the setup row, never the request.
- Never use `templ.Raw` or build HTML by string concatenation.
- Handlers, templ, htmx and JS follow [`docs/architecture/web-layers.md`](docs/architecture/web-layers.md).
- Product vocabulary follows `docs/domain/vocabulary.md` (`tools/sourcecheck` checks identifiers, file names and routes).
- UI strings go in `internal/web/i18n/locales/{en,ja}.toml`; use dotted,
  neutral IDs (`hello.title`, never a product word), and update both languages
  in the same PR. Templates use `i18n.T(ctx, "message.id")`.
- Update `docs/` in the same PR when terminology, invariants, data flow or
  dependency direction changes.

## Tests

- Module roots: table-driven unit tests, with no database.
- Stores and wiring: integration tests against a real PostgreSQL.
- `web`: unit tests without a DB for handlers, middleware and validation; real
  PostgreSQL only for end-to-end persistence or authentication, keeping most `web` tests fast.
- Authorization changes need a test proving an unauthorized user cannot see the data.
- Never delete, skip or weaken a test to pass CI; report it in the PR instead.

## Pull requests

- Maintainer AI branches use `claude/<topic>`, `codex/<topic>` or
  `argon/<topic>` (outside contributors' branches are unrestricted).
- Conventional Commits title; squash-merged. Keep diffs under ~400 lines
  excluding generated files; split larger work.
- Fill in the PR template briefly. UI changes state how to see them (`make dev`,
  URL, config such as `RIBBITTO_SIGNUP=on`) and what was checked in a browser.
  Attach screenshots only when the look is the point or the maintainer asks.
- Do not push to `main`. Merge only on the maintainer's explicit instruction
  ([workflow README](docs/workflow/README.md#what-no-ai-does)).
