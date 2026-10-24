# 42. Repository languages and formats need explicit approval

**Decided** (#1437, at the maintainer's request on 2026-10-19;
[workflow log](https://github.com/tkakkie/ribbitto/issues/2#issuecomment-6267383947)).
Amended by the maintainer on 2026-10-20 in #1437.
Amended by the maintainer on 2026-10-24 in #1695.
Use the language list below. A task needing another language, runtime,
dependency or format stops for the maintainer's explicit approval in its issue. The
path-and-kind table in [`tools/languagecheck`](../../tools/languagecheck/rules.go)
checks file kinds by location; this list is its human-readable description.
The table itself changes only through an issue approved by the maintainer.
[Decision 35](35-tooling-in-go.md) stays unchanged.

## Language list

Derived from all 976 tracked files of the 2026-10-19 baseline, without
exceptions, with later exact-path additions approved through #1437 and #1695.
Directory entries below are direct-only unless stated otherwise:

- Go throughout the repository, except fenced vendor and testdata directories.
- SQL migrations directly in `db/migrations/`, queries one module directory
  below `db/queries/`; templ in `internal/web/view/` and `internal/lintfixture/`.
- Versioned browser JavaScript (`<name>-v<N>.js`) directly in `web/static/`;
  CSS in `web/styles/` and `web/static/css/`; SVG in `web/static/brand/` and
  `web/static/channel-images-v1/`.
- Markdown recursively in `docs/`, directly in the GitHub issue-template,
  instructions and prompts directories, and the named root instructions,
  index, README and pull request template.
- YAML for the named root configurations, GitHub issue configuration and
  Dependabot, and directly in `.github/workflows/`; TOML directly in
  `internal/web/i18n/locales/`; the named JSON seed dataset in `cmd/seed/`.
- Go module manifests and checksums at the root, `tools/` and `tools/tbls/`;
  the named root licence, Makefile, git configuration and environment example;
  plain text API summaries directly in `docs/api/`, the two document exception
  files and the scope check's named allowlist.
- Vendor files only by their existing exact names: browser scripts, icon SVG,
  licences and README in `web/static/vendor/`. Guard Go test data only by these
  exact paths under the maintainer's decision recorded on #1437:
  `cmd/argonreview/testdata/implement-guard/cases.json`,
  `cmd/argonreview/testdata/implement-guard/hooks-off.json`,
  `cmd/argonreview/testdata/implement-guard/hooks-on.json`,
  `cmd/argonreview/testdata/implement-guard/settings-off.json`,
  `cmd/argonreview/testdata/implement-guard/settings-on.json`,
  `cmd/argonreview/testdata/implement-guard/mcp-config-on.json` (JSON), and
  `cmd/argonreview/testdata/implement-guard/real-agy-calls.jsonl` (JSONL).
  The six additions after `cases.json` were approved on 2026-10-20 in chat;
  the orchestrator records that approval on #1437. Other testdata is limited
  to the named browser shard totals and extension-less fuzz corpora below
  the conversation and identity modules, at `testdata/fuzz/Fuzz*/<file>`,
  with first line `go test fuzz v1`. Neither directory is excluded from checking;
  there is no general testdata, JSON or JSONL allowance.
- `cmd/argonreview/verifyimage/Dockerfile`: kind "verify image recipe", one
  file, direct. It may define only the image the trusted `verify` runner
  that #1696 adds will use, with only the tools the approved checks need:
  the generators (templ, sqlc, Tailwind; #1696) and PostgreSQL 18 (#1697).
  Adding a browser (Chromium) or any other package set to the recipe, or a
  second recipe, requires a new amendment of this decision approved by the
  maintainer in each case. The `test-browser` decision (#1698) only decides
  whether to propose such an amendment. No `.dockerignore`, compose or CI
  change, or script file.
  Every `FROM` uses an `@sha256:` digest; every download uses an exact version
  with a SHA-256 written in the recipe and checked in the same step; Go tools
  are built from the committed `tools/go.mod` and `tools/go.sum`; apt packages
  use exact versions, from a third-party repository only with its signing-key
  fingerprint written in the recipe; no secrets, no `ADD`, no `VOLUME`; the
  runner's image-ID constant that #1696 adds, not the recipe, is what the
  runner will trust.
  The path-and-kind table row lands with the file in #1696's recipe pull
  request; until then, `tools/languagecheck` keeps refusing the path.
  A Go test in `cmd/argonreview` (#1696) and tier A review check the recipe;
  neither the Go test nor the review is a security boundary. The boundary
  is the container flags and the image ID that are pinned in trusted code.

TypeScript, Node, Rust/WASM and any other language or runtime are **not
approved by this rule**; each becomes allowed only when its own accepted
decision adds it to the language list and path-and-kind table. TypeScript,
Node and the Go-driven esbuild build are the subject of #1356 and a separate
decision pull request still in review; a decision accepted through #1356
would add them. UI-library islands still need
[decision 40](40-plain-javascript-by-default-library-islands-by-measured-exception.md)'s
individual approval and measurement conditions; this gate grants no such
approval.

## Exceptions and checking

[`docs/language-exceptions.txt`](../language-exceptions.txt) records one exact
path, issue number and the maintainer's approving comment URL per line:

```text
<exact path> #<issue> https://github.com/tkakkie/ribbitto/issues/<issue>#issuecomment-<digits>
```

The URL must name that same issue, with no extra suffix. Only whitespace-only
lines and lines beginning exactly `# ` are ignored. A directory prefix,
trailing slash or glob metacharacter requires a trailing ` -- <reason>` with
nonempty text. This never expands a pattern: even with a reason, the path
must be one tracked file, so a directory cannot admit its descendants. Literal
metacharacters in a tracked filename can be explained this way. Duplicate,
untracked and already-allowed paths are stale and refused. Review verifies
that the linked comment is the maintainer's and approves that exact path;
the offline check cannot verify authorship.
The exceptions file itself must be tracked before it can approve any path.

The check reads NUL-separated tracked paths from git and the corresponding
working-tree files; missing files and read/list errors fail. Matching is
case-sensitive and component-based. Symlinks and special files are refused;
a shebang at the first two bytes is forbidden everywhere without exception.

The check does not judge commands, Makefile/CI/configuration contents or
dependencies; issue and pull request review remains the control for those.
The mechanical check catches accidental drift in file kinds, places and
shebangs, not deliberate evasion (such as a script under an allowed name or
kind), and is not a security boundary. A new language,
runtime or dependency needs the maintainer's explicit approval; an allowed
file path grants no such approval.

`make check` (through `check-tools`), `make prepush` and CI run the gate.
CI's checkout includes `.git`, even with shallow history. This is different
from Argon's isolated verification copy (#1429), which has no `.git` by
design and is not asked to run this check.

**Why:** a path-aware mechanical gate catches unapproved file kinds and
locations, including scripts hidden in fixtures or vendor directories.
The approval link leaves the human decision reviewable without networking
from a repository check.

**Considered:** extensions alone (admit SQL or JavaScript in the wrong place);
excluding vendor or testdata (hide scripts).
