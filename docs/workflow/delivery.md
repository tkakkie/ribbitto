Read when: briefing/waking implementers or publishing PR bodies.

# Delivering a pull request

## Standard implementer brief

Every implementer run (after `ready`) receives:

> You are `<Codex|Argon|Claude>`, implementing issue `#<N>` in `tkakkie/ribbitto` on
> branch `<ai>/<topic>` from `origin/main` (`ready`, risk: `<normal|high>`).
>
> Issue `#<N>` — `<title>`: `<body including Done when>`
>
> 1. Read `AGENTS.md`, touched-area docs and action-triggered workflow.
>    Use only your own token; no credentials in commits/PRs/comments/logs;
>    use only a disposable local test database (`RIBBITTO_TEST_DATABASE_URL`), never a shared one.
>    Token/ready/merge: docs/workflow/README.md#what-no-ai-does;
>    failures/DBs: docs/workflow/ai-credentials.md#tool-failures-and-disposable-databases;
>    PR scan: docs/workflow/delivery.md#pr-body-credential-scan;
>    measurements: docs/workflow/investigations.md#investigations-and-the-performance-target.
> 2. Implement in this worktree under ~400 lines excluding generated files.
>    Stop and report for maintainer decisions, new languages/tools or
>    reclassification (docs/workflow/README.md#roles-and-running-the-ais).
>    For a behaviour-preserving script-to-module PR, compare baseline-SHA
>    old script and new module outputs on identical inputs (clock, storage failures,
>    malformed values included): `node --test` in `web/test` for DOM-free logic,
>    `TestBrowser` cases for browser behaviour. PR body records
>    baseline SHA, commands/result, or what restructuring compared without an
>    old/new pair; no permanent evidence file
>    ([#2](https://github.com/tkakkie/ribbitto/issues/2#issuecomment-6559340753)).
> 3. Run local checks (docs/workflow/delivery.md#local-checks-by-change-type),
>    plus `make deps` for import changes. Fix failures without deleting, skipping or weakening tests.
> 4. Commit (Conventional Commits, `Co-Authored-By:` trailer), push only your branch;
>    scan the body (docs/workflow/delivery.md#pr-body-credential-scan); open a DRAFT PR using
>    `.github/pull_request_template.md`, with `Closes #<N>`.
> 5. Wait for CI (`gh pr checks`); fix failures (`make prepush` before follow-up
>    pushes), stop at green. Do not set labels, request/run reviews, mark ready,
>    merge, or touch `main`, other branches, settings or tokens.

## Local checks by change type

Each push: classify the whole diff against `main`; mixed/unlisted changes and `go.mod`, `go.sum`, `tools/webbuild`, `tools/sourcemaps`,
`tools/webtypes`, locales, `Makefile`, configuration,
`web/static/vendor/README.md` and generated docs (`docs/api`,
`docs/dependencies.md`, `docs/schema`) use the last row. Run a new row's opening checks before pushing.

`make prepush` before **every** push; additional opening checks:

| Change (exclusive rows) | Additional opening checks |
|---|---|
| Markdown in `docs/`/root; `docs/size-exceptions.txt` (exclusions above) | `make check-tools` |
| `.github/**` only | `make check-tools`; `go test -race ./web/static` (reads workflows) |
| Other `tools/**` only | `make check-static check-tools` |
| Root-module `*_test.go` only; fixture/helper packages and `testdata/` use last row | `make check-static check-tools`; `make check-test GO_TEST_PACKAGES="<touched packages>"` with DB required below |
| Anything else | `make check`; DB/Node requirements below |

DB runs require disposable local `RIBBITTO_TEST_DATABASE_URL` and
`RIBBITTO_REQUIRE_DB=1` (else tests silently skip). For TypeScript/toolchain (`web/src`, `web/test`, `tools/webtypes`, `tools/webbuild`,
`tsconfig.json`, any `package.json`/`package-lock.json`, `.node-version`),
first `make web-install`, then `RIBBITTO_REQUIRE_NODE=1 make check-web test-web`.

CI remains the full gate. **Verified by** names the row and actual commands;
a subset, `NOT VERIFIED` or skipped checks (no Node/DB) is never a passing
`make check`.

## Model and effort

Record model/effort and conditions on issue/PR or brief; never by quota
or maintainer judgment.
Codex: `gpt-6.1-sol`, `medium`; `high` if *Done when* or *Notes* cites:

| ID | Condition |
|---|---|
| H1 | Authentication, authorisation, organisation separation or sessions change |
| H2 | Database integrity, migrations, transactions or locks change |
| H3 | SSE reconnect, event order, deduplication or delivery changes |
| H4 | A security, correctness or CI verification condition changes: a gate is added, removed or loosened |
| H5 | A behaviour-preserving conversion has no old/new comparison test in *Done when* |
| H6 | Changes to concurrent paths sharing one state (ordering, locking, atomicity); cache contents, keys or invalidation; whether a background job runs, retries, runs once or stops cleanly |

With a comparison test, conversions are `medium` unless H1–H4/H6 holds;
[comparison stays mandatory](#standard-implementer-brief) (#2052).
Reading/moving/testing unchanged behaviour is not H6; the
[third-AI requirement](README.md#lifecycle) stays. Switch to `high` when
H1–H6 first holds. Line/file count and issue length alone never qualify. Codex reviews/checks: `gpt-6-astra`; no implementation rule for it.

Argon: only `gemini-4-argon-high`. Claude: Sonnet for orchestration, issues,
docs, ordinary reviews; Opus only for high-risk review round 1 or when named
by the maintainer.

## Quota substitution

Quota error (`RESOURCE_EXHAUSTED`, etc.): record, never retry that AI.
Other failures: record, retry once, then ask the maintainer.

- **Implementation:** replacing a named AI needs the maintainer. Otherwise:
  Codex/Argon → the other, then Claude (docs-only); Claude → Codex, then Argon.
  None available → ask the maintainer.
  **Exception:** Codex exhausted on rule-1 work with H1–H4/H6 needs maintainer
  confirmation before Argon takes over. All others, including Argon-to-Codex, are automatic.
- Continue the branch; record why on issue/PR. Partial work, pushed or not,
  counts as authorship for independence.
- **Review:** reselect under [independence](reviewing.md#review-independence-and-rounds):
  AIs with quota that neither wrote nor changed the work; third AI also excludes
  the first reviewer. None → ask the maintainer.
  Required reviews, round caps and maintainer gates stay unchanged.

## Waking the orchestrator

One blocking background command (no `nohup`/trailing `&`) wakes the
orchestrator on completion/timeout (#1457). Read results; continue to
`ai-reviewed` until a rule in this file, [lifecycle](README.md#lifecycle),
[reviewing](reviewing.md), [operations](review-operations.md) or
[follow-ups](review-followups.md) says to ask the maintainer.
Report maintainer decisions immediately; on failure/timeout inspect branch
and worktree, then [substitute or retry once](#quota-substitution), else report.
CI/Copilot waits: 60 minutes max; report and stop.

## PR body credential scan

Before publishing a PR body, scan it; review matches and redact credentials.
Harmless prose/placeholders may stay. Joins `\`-continued lines; flags URL user info (#358):

```sh
sed -e :a -e '/\\$/{' -e '$!N' -e '}' -e 's/\\\n//' -e ta body.md |
  grep -niE '://[^/@[:space:]]*@|password|passwd|secret|token|(api|access|private)[-_[:space:]]?key'
```

