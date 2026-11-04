Read when: briefing/waking implementers or publishing PR bodies.

# Delivering a pull request

## Standard implementer brief

Every implementer run (after `ready`) receives:

> You are `<Codex|Argon|Claude>`, implementing issue `#<N>` in `tkakkie/ribbitto` on
> branch `<ai>/<topic>` from `origin/main` (`ready`, risk: `<normal|high>`).
>
> Issue `#<N>` — `<title>`: `<body including Done when>`
>
> 1. Read `AGENTS.md`, only touched-area docs and action-triggered workflow:
>    only your own token, no credentials in commits/PRs/comments/logs;
>    [token/ready/merge](README.md#what-no-ai-does),
>    [tool failures/disposable local DBs](ai-credentials.md#tool-failures-and-disposable-databases),
>    [PR scan](#pr-body-credential-scan), [measurements](investigations.md).
> 2. Implement under ~400 lines excluding generated files. Stop for maintainer
>    decisions, new languages/tools or [reclassification](README.md#roles-and-running-the-ais).
>    Before a behaviour-preserving script-to-module PR, compare baseline-SHA
>    old script and new module on identical inputs (clock, storage failures,
>    malformed values included): `node --test` in `web/test` for DOM-free logic,
>    existing/new `TestBrowser` cases for browser behaviour. PR body records
>    baseline SHA, commands/result, or what restructuring compared without an
>    old/new pair; no permanent evidence file
>    ([#2](https://github.com/tkakkie/ribbitto/issues/2#issuecomment-6559340753)).
> 3. Run [local checks](#local-checks-by-change-type), plus `make deps` for
>    import changes. Fix failures without deleting, skipping or weakening tests.
> 4. Commit (Conventional Commits, `Co-Authored-By:`), push only your branch;
>    [scan](#pr-body-credential-scan) the body file and open a DRAFT PR using
>    `.github/pull_request_template.md`, with `Closes #<N>`.
> 5. Wait for CI (`gh pr checks`); fix failures (`make prepush` before follow-up
>    pushes), stop at green. Do not set labels, request/run reviews, mark ready,
>    merge, or touch `main`, other branches, settings or tokens.

## Local checks by change type

Classify the whole diff against `main` each push; mixed/unlisted changes use
the last row, as do `go.mod`, `go.sum`, `tools/webbuild`, `tools/sourcemaps`,
`tools/webtypes`, locales, `Makefile`, configuration,
`web/static/vendor/README.md` and generated docs (`docs/api`,
`docs/dependencies.md`, `docs/schema`). A new row needs its opening checks
before pushing.

Run `make prepush` before **every** push, plus these checks before opening:

| Change (exclusive rows) | Additional opening checks |
|---|---|
| Markdown under `docs/` and root documents; `docs/size-exceptions.txt`, excluding generated docs above | `make check-tools` |
| `.github/**` only | `make check-tools`; `go test -race ./web/static` (reads workflows) |
| Other `tools/**` only | `make check-static check-tools` |
| Root-module `*_test.go` only; fixture/helper packages and `testdata/` use last row | `make check-static check-tools`; `make check-test GO_TEST_PACKAGES="<touched packages>"` with DB required below |
| Anything else (production Go, TypeScript, templ, CSS, SQL, generated files) | `make check` with DB required below; TypeScript/toolchain changes also require Node below |

DB-required runs set disposable local `RIBBITTO_TEST_DATABASE_URL` and
`RIBBITTO_REQUIRE_DB=1` (otherwise tests skip silently). For TypeScript or
its toolchain (`web/src`, `web/test`, `tools/webtypes`, `tools/webbuild`,
`tsconfig.json`, any `package.json`/`package-lock.json`, `.node-version`),
first `make web-install`, then `RIBBITTO_REQUIRE_NODE=1 make check-web test-web`.

CI remains the full gate. **Verified by** names the row and actual commands;
a subset, `NOT VERIFIED` or skipped checks (no Node/DB) is never a passing
`make check`.

## Model and effort

The orchestrator records model/effort and conditions in the work record
(issue/PR or brief), never by remaining quota or maintainer judgment.
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
Reading/moving/testing without behaviour changes is not H6; the
[third-AI requirement](README.md#lifecycle) stays. Switch to `high` when
H1–H6 first holds during work. Line/file count and issue length never qualify
alone. Codex reviews/checks use `gpt-6-astra`; no implementation rule for it.

Argon: only `gemini-4-argon-high`. Claude: Sonnet for orchestration, issues,
docs, ordinary reviews; Opus only for high-risk review round 1 or when named
by the maintainer.

## Quota substitution

Quota error (e.g. `RESOURCE_EXHAUSTED`): record, never retry that AI.
Other failures: record, retry once, then ask the maintainer.

- **Implementation:** a named implementer needs the maintainer. Otherwise
  substitute in order: the other of Codex/Argon, then Claude only for docs-only;
  from Claude (docs-only), Codex then Argon. None available → ask the maintainer.
  **Exception:** Codex exhausted on rule-1 work with H1–H4/H6 needs maintainer
  confirmation before Argon takes over. All other substitutions, including
  every Argon-to-Codex one, are automatic.
- Continue the branch; record why on issue/PR. Partial work, including any
  AI's pushed changes, counts as authorship for independence.
- **Review:** choose again under [independence](reviewing.md#review-independence-and-rounds)
  from AIs that neither wrote nor changed the work and have quota. The third
  AI also excludes the first reviewer. If none remains, ask the maintainer.
  Required reviews, round caps and maintainer gates stay unchanged.

## Waking the orchestrator

One blocking background command (no `nohup`/trailing `&`) wakes the
orchestrator on completion/timeout (#1457). Read results; continue a PR to
`ai-reviewed` under [lifecycle](README.md#lifecycle), [reviewing](reviewing.md),
[operations](review-operations.md) and [follow-ups](review-followups.md).
Report maintainer decisions immediately; on failure/timeout inspect branch
and worktree, then [substitute or retry once](#quota-substitution), else report.
CI/Copilot waits cap at 60 minutes: report and stop.

## PR body credential scan

Before publishing a PR body (`gh pr create --body-file body.md`), scan it:
review every match, redact credentials; harmless prose/placeholders may stay.
It joins `\`-continued lines and flags URL user info (#358):

```sh
sed -e :a -e '/\\$/{' -e '$!N' -e '}' -e 's/\\\n//' -e ta body.md |
  grep -niE '://[^/@[:space:]]*@|password|passwd|secret|token|(api|access|private)[-_[:space:]]?key'
```

