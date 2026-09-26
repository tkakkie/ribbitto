# AGENTS.md

Rules for AI coding tools and the people using them. Keep this file short;
explanations of the product and the code belong in `docs/`.

## Project

ribbitto is a self-hostable team chat: Go, `net/http`, templ, htmx with
Server-Sent Events, Tailwind CSS, PostgreSQL (pgx, sqlc, goose). One
maintainer, public repository. Everything in the repository — code,
comments, docs, commits, issues, pull requests — is written in English.

## Workflow

Follow [`docs/workflow.md`](docs/workflow.md). The rules you must not skip:

- **Start of a session:** read the pinned Status issue (`gh issue view 1`)
  and `docs/roadmap.md`.
- **Every issue and pull request an AI writes (including the docs it
  changes) is reviewed by the other AI (Claude ↔ Codex) before the
  maintainer is asked.** At most two review rounds; then ask the
  maintainer.
- Issues start from a template in `.github/ISSUE_TEMPLATE/`; keep its
  headings. No implementation without the `ready` label.
- **End of a unit of work:** overwrite the Status issue body. When the
  workflow goes wrong, comment on the AI workflow log (#2).

## Commands

- `go build ./... && go vet ./...` — must pass before you open a pull request.
- `gofmt -l .` must print nothing.

(`make check` replaces these once the Makefile exists.)

## Layout and dependency direction

`cmd/ribbitto` wires everything. Under `internal/`: `domain` (no internal
imports), `app` (use cases and the only authorization logic; imports
`domain`), `infra/postgres` (implements `app` interfaces), `realtime`
(imports `domain`; receives authorization, rendering and event reading as
interfaces), `web` (handlers and templ; imports `domain`, `app`,
`realtime`, never `infra`). Handlers call use cases that return plain
structs; only `web` produces HTML. Do not add an import that breaks this.

## Writing code

- Write boring Go: no generics or reflection unless they remove real
  duplication, no global state, pass `context.Context`, wrap errors with
  `fmt.Errorf("doing x: %w", err)`.
- Every package under `internal/` has a `doc.go` stating its
  responsibility. Command packages (`cmd/...`) use the package comment in
  their main file instead; do not add empty `doc.go` files. Exported
  identifiers have doc comments.
- Comments explain **why**, not what. Record the reason for a non-obvious
  choice, a constraint, or a trap; do not narrate the code.
- Organisation-owned data is always scoped by `organization_id`; the
  organisation comes from the URL, never from a request body; a
  non-member gets 404.
- Never use `templ.Raw` or build HTML by string concatenation.
- Product vocabulary (ribbit, pond, marsh…) appears only in UI message
  files, never in identifiers.
- When a change alters terminology, invariants, data flow or dependency
  direction, update `docs/` in the same pull request.

## Tests

- `domain` and `app`: table-driven unit tests.
- `infra`: integration tests against a real PostgreSQL.
- `web`: unit tests for handlers, middleware and validation that need no
  database; a real PostgreSQL only for cases that involve persistence or
  authentication end to end. Keep most `web` tests fast and DB-free.
- Authorization changes need a test proving that someone who must not see
  the data does not see it.
- Never delete, skip or weaken a test to make CI pass; say so in the pull
  request instead.

## Pull requests

- Maintainer AI branches use `claude/<topic>` or `codex/<topic>`. Branch
  names of outside contributors are not restricted.
- Conventional Commits title; squash-merged. Keep the diff under about 400
  lines excluding generated files; split larger work.
- Fill in the pull request template briefly. UI changes include a
  screenshot.
- Do not push to `main` and do not merge pull requests; the maintainer
  merges.
- When running `codex`, `grok` or `claude -p` headless, close stdin
  (`< /dev/null`) and set a time limit, or they can wait forever
  (see `docs/workflow.md#running-the-other-ai`).
