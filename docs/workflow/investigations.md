# Investigations and the performance target

- New measurement or verification stages, prototypes and measurement
  infrastructure need explicit maintainer approval before work starts.
- Investigation and optimisation issues state a pull-request or time cap
  before work starts. Stop and report at the cap, even without a conclusion.
- The target is 5,000 concurrent connections per process, with message arrival
  not much worse than without unread counts. Check 2 vCPU / 4 GB in a separate
  issue once available; development-machine runs decide until then.

## Host measurements

An approved performance measurement is the only standing exception to the
[handoff rule](running-other-ai.md#handoff-of-another-ais-code) (maintainer, 2026-10-18, #1308).
After handoff, ordinary builds, tests and `make dev` stay in CI or sandboxes.
All conditions must hold:

1. Cross-review (Claude or Codex), Copilot and adversarial review approve
   with measurement open; CI passes at that head, then the PR gets
   `ai-reviewed`. Measure after that label. Before asking for merge, the
   orchestrator checks results against the issue's *Done when*.
2. Match the full build/run SHA to reviews and CI, not just the label. Each
   push needs reviews and CI at the new SHA before another run. Until the
   one-time setup exists, each run also needs case-by-case maintainer
   approval for that SHA, as for #1273 and #1276.
3. The measurement is required by the issue's *Done when*, approved by
   `ready`, and within the issue's cap.
4. Run only the PR's `cmd/ribbitto` server binary. Seed, migrations, harness
   and other tools come from the trusted, fixed base SHA or a source
   approved in advance (e.g. an external harness). If head adds a migration,
   stop for the maintainer.
5. Use the isolated cross-compile below first; only the finished macOS
   binary reaches the host. Both routes build head and base from fresh,
   verified clean checkouts of approved full SHAs with
   the [trusted invocation's git settings and `GOENV=off`](running-argon.md#the-trusted-invocation).
   Never build from the implementer's worktree or sandbox: ignored/untracked
   files and local git config can alter builds without changing `HEAD`.
   Use embedded assets; no head tree is needed at run time.
6. If an isolated build is impossible, build as the dedicated low-privilege
   user with the minimal environment below. Use the same route for base.
7. Run as that user under the file/network profile below; `env -i` alone
   restricts neither. Allow only the binary, its data, system runtime reads,
   server loopback bind/accept and throwaway database loopback TCP. Exclude
   shared databases and Chrome's DevTools port. The maintainer creates the
   user (a system change) and installs/validates the profile once. If this
   fails, stop for a case-by-case maintainer decision; keep restrictions.
8. Listen only on `127.0.0.1`; use a throwaway database container, published
   only on its chosen `127.0.0.1` port, with disposable data and credentials.
9. Afterwards, including failures, stop all servers; remove the
   database/container, volume, checkouts/worktrees, caches and binaries.
   Keep only the one-time setup and results.
10. Record SHAs, conditions (toolchain, image digest, restrictions), exact
    commands, results and that this was a host measurement in the PR.
    Redact credentials.

**Reproducibility (#1149).** Alternate base and head in one session on the
same host, Chrome, profile and dataset. Use the same build/run route,
toolchain, flags, user and restrictions; only code differs. Start only at
a 1-minute load below 4; exclude warm-up posts. Absolute values from
another session never decide a result alone.

### Build routes

The approved operator uses these outside the implementer's sandbox with
trusted git/Go and a trusted Docker Go image pinned by digest. Set `sha` to
the full approved head or fixed trusted base SHA; repeat in a new checkout.
Reuse no local clone, config, hooks or cache. Follow the
[trusted invocation](running-argon.md#the-trusted-invocation), also disabling
hooks and fsmonitor:

```sh
session=$(mktemp -d /private/tmp/ribbitto-measure.XXXXXX) &&
  src=$session/src && mkdir "$src" "$session/out" &&
  pinned() { env -i PATH="$PATH" GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_NO_REPLACE_OBJECTS=1 git -C "$src" -c core.hooksPath=/dev/null -c core.fsmonitor=false "$@"; } &&
  pinned init --quiet --template= &&
  pinned fetch --quiet https://github.com/tkakkie/ribbitto.git "$sha" &&
  replaced=$(pinned for-each-ref refs/replace) && test -z "$replaced" &&
  pinned checkout --quiet --detach "$sha" &&
  test "$(pinned rev-parse HEAD)" = "$sha" &&
  tree_status=$(pinned status --porcelain --ignored --untracked-files=all) &&
  test -z "$tree_status"
```

Continue only on success. Cross-compile in a throwaway container first;
`image` is the trusted image's digest reference. Check modules against this
SHA's `go.sum`. Mount only read-only source and empty output; no host home,
credentials or Docker socket:

```sh
docker run --rm --read-only --cap-drop=ALL --security-opt=no-new-privileges \
  --user "$(id -u):$(id -g)" --tmpfs /tmp:exec,mode=1777 \
  --mount "type=bind,src=$src,dst=/src,readonly" \
  --mount "type=bind,src=$session/out,dst=/out" --workdir /src \
  --entrypoint /usr/bin/env "$image" -i PATH=/usr/local/go/bin:/usr/bin:/bin \
  HOME=/tmp GOENV=off GOTOOLCHAIN=local GOCACHE=/tmp/build GOMODCACHE=/tmp/mod \
  GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_NO_REPLACE_OBJECTS=1 \
  CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
  go build -mod=readonly -trimpath -buildvcs=false -o /out/ribbitto ./cmd/ribbitto
```

Use the actual host architecture for both SHAs. Run no source-tree command
except this Go build; do not regenerate assets or run PR binaries there.
Before staging, record SHA and `shasum -a 256`; `-buildvcs=false` avoids
container git ownership differences.

If this cannot build a usable binary, record why. The maintainer stages
freshly verified source read-only in dedicated `build`, empty of old
output/caches; no symlink may escape. The dedicated user writes only
output/caches there. Recheck staged `HEAD` and clean status with `pinned`
(`src` is staged source). Set `go` to the absolute trusted Go executable;
match the container's version and flags:

```sh
cd /private/var/ribbitto-measure/build/src &&
  sudo -u _ribbitto_measure /usr/bin/env -i PATH=/usr/bin:/bin \
  HOME=/private/var/ribbitto-measure/build/cache GOENV=off GOTOOLCHAIN=local \
  GOCACHE=/private/var/ribbitto-measure/build/cache/go-build \
  GOMODCACHE=/private/var/ribbitto-measure/build/cache/mod \
  GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_NO_REPLACE_OBJECTS=1 \
  CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
  "$go" build -mod=readonly -trimpath -buildvcs=false \
  -o /private/var/ribbitto-measure/build/out/ribbitto ./cmd/ribbitto
```

### One-time setup and run

The maintainer, not the implementing AI, sets up:

- Create `_ribbitto_measure`, a non-admin user with no login, credentials,
  privileged groups or access to Docker, developer homes or shared data.
- Create administrator-owned `/private/var/ribbitto-measure` with a read-only
  `server` binary slot and user-writable `data`. Keep fallback `build`
  separate, unreadable by the server profile. Use real paths, no symlinks.
- Install the profile below, administrator-owned and not writable by the
  measurement user, at `/private/var/ribbitto-measure/server.sb`. Reserve
  server port 18080 and throwaway database port 55434 for these alone.
  Change ports in profile and commands together; never allow all loopback.
- Validate on this macOS version as the dedicated user with the trusted
  base, in this order. Watch denials with
  `log stream --style compact --predicate 'sender == "Sandbox"'`:
  1. Without a database:
     `sandbox-exec -f /private/var/ribbitto-measure/server.sb /private/var/ribbitto-measure/server bogus`.
     Expect logged usage and exit 1: profile, dyld and Go start. Use the
     run command's user/environment below, replacing `serve`.
  2. Start with the throwaway database; make a database-backed request to
     `127.0.0.1:18080`, watching the log. Possible gaps: self-signals
     (`(allow signal (target self))`), parent-directory metadata reads and
     pgx's current-user lookup. Prefer explicit `user=` and `sslmode=disable`
     in `db_url` to allowing `mach-lookup`.
  3. In a separate test copy, add only execution/runtime reads for `/bin/cat`
     and `/usr/bin/nc`; keep other file/network rules. With the same
     user/environment, confirm outside reads (including operator's `~`) and
     connections to 5432, 55433 and Chrome's DevTools port fail. Also check
     other programs cannot execute and other addresses/ports cannot bind.
  Record results and profiles in the PR. A denial is a stop; every addition
  needs review and maintainer approval before widening the profile.

Minimal profile: system library/device reads exclude developer files.
No broad profile imports. SBPL requires `localhost`, not numeric hosts;
it matches only loopback. Commands still use `127.0.0.1`:

```scheme
(version 1)
(deny default)
(allow process-exec (literal "/private/var/ribbitto-measure/server"))
(allow sysctl-read)
(allow file-read* file-map-executable
  (literal "/private/var/ribbitto-measure/server")
  (subpath "/usr/lib") (subpath "/System/Library/dyld")
  (subpath "/System/Volumes/Preboot/Cryptexes/OS/usr/lib")
  (subpath "/System/Volumes/Preboot/Cryptexes/OS/System/Library/dyld"))
(allow file-read* file-write* (subpath "/private/var/ribbitto-measure/data"))
; dyld opens / as its lookup root.
(allow file-read* (literal "/") (literal "/dev/null") (literal "/dev/urandom"))
(allow network-bind (local tcp "localhost:18080"))
(allow network-inbound (local tcp "localhost:18080"))
(allow network-outbound (remote tcp "localhost:55434"))
```

For each SHA, with servers stopped, stage its checksummed binary read-only,
clear `data` and restore the same dataset with trusted base tools only.
Start the throwaway database at `127.0.0.1:55434`; set `db_url` to its
disposable connection string with that numeric host/port, explicit `user=`
and `sslmode=disable`, no fallback hosts or Unix sockets. Run in the
foreground with no inherited open files except standard input/output/error:

```sh
cd /private/var/ribbitto-measure/data &&
  sudo -u _ribbitto_measure /usr/bin/env -i PATH=/usr/bin:/bin \
  HOME=/private/var/ribbitto-measure/data TMPDIR=/private/var/ribbitto-measure/data \
  TZ=UTC RIBBITTO_ADDR=127.0.0.1:18080 RIBBITTO_DATABASE_URL="$db_url" \
  /usr/bin/sandbox-exec -f /private/var/ribbitto-measure/server.sb \
  /private/var/ribbitto-measure/server serve < /dev/null
```

Trusted harness/Chrome run separately as the operator, never as the server
user; their DevTools port stays denied. Stop the server before swapping
binaries; apply condition 9 after the session, including errors.
