# Codex Node setup and sandbox evidence

[Decision 43](../decisions/43-typescript-with-a-go-driven-build.md) limits
Node/npm to type checking, unit tests and dependency management in development,
CI and implementer sandboxes. Neither asset builds nor run time use Node.
Argon gains no Node/npm commands; its `verify` and permissions are unchanged.
Generated-asset packages stay CI-only / `NOT VERIFIED` in
[Argon verify](argon-verify.md#results); container asset generation or any checklist
change needs its own `process` issue.

## Routine use and trusted setup

Routine Codex Node use is allowed from the merge of #1415, with #1410 already
merged. The one-time #1410 exception ended with its last PR; it does not
authorize routine use. The separate, once-only probe below was approved as a
new verification stage by #1415's `ready`, under
[investigations](investigations.md#investigations-and-the-performance-target).

Before a run, trusted maintainer setup supplies the host Node pinned by the
checkout's `.node-version` (26.8.1 when probed), with that release's bundled
npm beside it on `PATH`. Never download Node from a sandbox or expand its
network profile. The [implementation profile](running-other-ai.md#codex-permissions)
already allows `registry.npmjs.org`; this adds no domain or permission.
Report a missing or mismatched runtime; never work around it.

Install only from the reviewed lockfile through `make web-install`
(`go -C tools run ./webtypes install ..`): the wrapper runs
`npm ci --ignore-scripts`, includes dev/optional packages, fixes the registry
and uses empty user/global npm configs. It keeps the cache in `bin/.cache/npm`
and install writes in the checkout. No npm scripts. `make check-web` checks
without installing; missing/mismatched Node or checker reports `NOT VERIFIED`
unless `RIBBITTO_REQUIRE_NODE=1` makes it fail. `make check` includes that check.
Unit-test commands belong to #1412; none is introduced here.

After a reviewed pin-update PR merges, the maintainer updates the trusted host
installation outside the sandbox to the new `.node-version`, including its
bundled npm, and checks the selected `PATH` before another run. Follow the
[pin-update procedure](../architecture/web-toolchain.md#versions-and-installation--1410-1415)
for the lockfile and CI proof. Do not separately upgrade global npm. The
wrapper compares the runtime version on every invocation; a mismatch fails
installation or reports an unverified check. This does not revalidate the
host installation's provenance on every run.

Delete `node_modules` before handoff. The
[handoff prohibition](running-other-ai.md#handoff-of-another-ais-code) stays in
force; CI remains the check. Measurement builds follow the existing
[isolated build routes](investigations.md#build-routes).

## Recorded probe — 2026-10-26

The orchestrator ran this **once** in a scratch checkout of `origin/main`
`39963c2` outside the repository.
It used Codex's implementation `perms` profile, model `gpt-6-astra`. The local
date was 2026-10-26; the shell's UTC date was 2026-10-25. This records supplied
evidence, not a rerun by the documentation implementer. Commands/output are
recorded on [#1415](https://github.com/tkakkie/ribbitto/issues/1415#issuecomment-6404855420).

The probe used the four checkout-local Go caches from the
[implementation brief](running-other-ai.md#implementation-brief).
Relevant sandbox environment names were `npm_config_http_proxy`,
`npm_config_https_proxy`, `npm_config_noproxy`, `npm_config_proxy`,
`NODE_USE_ENV_PROXY` and `NODE_USE_SYSTEM_CA`; no values are reproduced.
Home is written as `~`; no usernames, machine names or credentials are recorded.

Command forms below summarize the supplied record; they are not instructions
to repeat the approved once-only probe. Before/after home listings were
compared without publishing their contents.

| Step / commands | Recorded result |
|---|---|
| `cat .node-version`; `command -v node npm`; `node --version`; `npm --version` | Pin `26.8.1`; both executables from the trusted host installation. Node `v26.8.1` equals `v` + pin; npm `11.19.0`. |
| Before: list home and default cache `~/.npm`, count entries, size and files newer than `.node-version`; check `bin/.cache` | Home: 50 entries. Cache entries: `_cacache`, `_logs`, `_npx`, `_update-notifier-last-checked`; 1094 files/directories, 136312 KiB, 0 files newer than the pin. Checkout `bin/.cache` absent. |
| `make web-install` | Exit 0: “added 2 packages, and audited 3 packages”; “found 0 vulnerabilities”. Wrapper used `npm ci --ignore-scripts`. |
| After: compare home listing and repeat default-cache counts/size/newer-file check; inspect checkout install/cache and lockfile | Home diff empty. Default cache unchanged: 1094 entries, 136312 KiB, 0 newer files. Checkout `node_modules`: 31360 KiB, only `@typescript/typescript-darwin-arm64` native package installed, `tsc` binaries present. Checkout `bin/.cache/npm`: 10380 KiB. Lockfile: 0 `hasInstallScript` lines; 3 lines each for `@typescript/typescript-linux-x64` and `@typescript/typescript-darwin-arm64`. |
| `RIBBITTO_REQUIRE_NODE=1 make check-web` | Exit 0. |
| Node absent: `PATH` set to a scratch directory then `/usr/bin:/bin`; `command -v node`; `RIBBITTO_REQUIRE_NODE=1 make check-web`; `make check-web` | No Node found. Required check: `Node 26.8.1 unavailable: exec: "node": executable file not found in $PATH`, make exit 2. Optional check: `NOT VERIFIED: client TypeScript: Node 26.8.1 unavailable: ...`, exit 0. |
| Wrong version: scratch directory first on `PATH`; `make web-install` | `Node version "v26.0.0" does not match 26.8.1`, make exit 2. The wrapper rejected the version before npm (its `ready()` check precedes npm in `tools/webtypes/main.go`); `node_modules` directory metadata identical before/after. |
| Control: `touch` a file in `~` and one above the probe directory; write inside workspace | Both outside writes refused: `Operation not permitted`, exit 1; workspace write succeeded. |
| `curl https://example.com`; `curl https://registry.npmjs.org/typescript` | First refused by tool layer: “domain is not on the allowlist for the current sandbox mode”; no curl exit status or HTTP code. Registry returned 200. |
| `rm -rf node_modules`; scratch cleanup | Install removed; scratch directory deleted afterwards. |

Go reported downloading its toolchain `go1.27.3` (darwin/arm64). npm warned
that env config `http-proxy` was unknown and advertised npm `12.2.1`.
In `tools/webtypes/main.go`, the wrapper's install invocation is:

```sh
npm ci --ignore-scripts --include=optional --include=dev \
  --registry=https://registry.npmjs.org/ \
  --userconfig=<empty checkout-local file> --globalconfig=<empty checkout-local file>
```

The wrapper's check invokes `node node_modules/typescript/bin/tsc --project
tsconfig.json`; the wrapper first uses `node --version`. These command forms
explain the record, including indirect Node/npm execution through Go.

## Limits of the evidence

This is a recorded host-profile probe, not a Go test: CI cannot reproduce the
maintainer's sandbox. It records the wrapper's behavior in that run only.

Not verified: the `linux-x64` package or GitHub Actions,
lifecycle scripts (every install uses `--ignore-scripts`, and the lockfile has
no `hasInstallScript` entry), or future Node/npm versions. The transcript
establishes no host Node provenance beyond its location on the trusted host `PATH`.
The wrong-version check compared `ls -ld` directory metadata only, not file
contents. Default-cache entry counts, sizes and newer-file counts plus the
home-listing diff are the recorded observations, not a file-content comparison
of the home or proof about future installs.
