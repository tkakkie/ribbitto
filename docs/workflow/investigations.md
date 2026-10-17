# Investigations and the performance target

- New measurement or verification stages, prototypes and measurement
  infrastructure need explicit maintainer approval before work starts.
- Investigation and optimisation issues state a pull-request or time cap
  before work starts. Stop and report at the cap, even without a conclusion.
- The target is 5,000 concurrent connections per process, with message arrival
  not much worse than without unread counts. Check 2 vCPU / 4 GB in a separate
  issue once available; development-machine runs decide until then.

## Host measurements

The maintainer's decision (2026-10-18, #1308) makes an approved performance
measurement the only exception to the [handoff rule](running-other-ai.md#handoff-of-another-ais-code).
Ordinary builds, tests and `make dev` remain CI-only or sandbox-only after
handoff. Every condition must hold:

1. Required cross-review (Claude or Codex), Copilot and adversarial review
   approve the code with the measurement item still open; CI passes at the
   head under test, then the PR carries `ai-reviewed`. Measurement follows
   that label. Before asking for merge, the orchestrator checks the recorded
   result against the issue's *Done when*.
2. Check the full SHA to build and run against the SHA covered by those
   reviews and CI; the label alone is insufficient. Every subsequent push
   needs reviews and CI at the new SHA before another host run. Until the
   one-time setup below exists, each host measurement also needs the
   maintainer's case-by-case approval for that SHA, as for #1273 and #1276.
3. The measurement is required by the issue's *Done when*, approved by
   `ready`, and within the issue's cap.
4. Only the PR's `cmd/ribbitto` server binary runs. Seed, migrations, the
   harness and all other tools come from the trusted, fixed base SHA or
   something approved in advance, such as the harness outside the repository.
5. Use the isolated cross-compile below first; only the finished macOS
   binary reaches the host. Both head and base builds, by either route,
   start from fresh, verified clean checkouts of the approved full SHAs with
   the [trusted invocation's git settings and `GOENV=off`](running-argon.md#the-trusted-invocation).
   Never build from the implementer's worktree or sandbox: ignored or
   untracked files and local git configuration can alter a build without
   changing `HEAD`. Use embedded assets; no head tree is needed at run time.
6. If an isolated build is impossible, build as the dedicated low-privilege
   user with the minimal environment below. Use the same route for base.
7. Run as that user with file and network restrictions: `env -i` alone
   restricts neither. The profile below allows the binary, its data and
   required system runtime reads, binding/accepting only on the server's
   loopback port, and outbound TCP only to the throwaway database's loopback
   port. Shared databases and Chrome's DevTools port are excluded.
   The maintainer creates the user and installs/validates the profile once;
   creating a user changes system settings. If restrictions cannot be
   applied, stop for a case-by-case maintainer decision; do not drop them.
8. Listen only on `127.0.0.1`; use a throwaway database container, published
   only on its chosen `127.0.0.1` port, with disposable data and credentials.
9. Afterwards stop all servers and remove the database/container and its
   volume, checkouts/worktrees, build caches and binaries, including failed
   runs. Retain only the one-time setup and recorded results.
10. Record the SHAs run, conditions (including toolchain, image digest and
    restrictions), exact commands, results and that this was a host
    measurement in the PR. Redact credentials.

**Reproducibility (#1149).** Alternate base and head in one session on the
same host, Chrome, profile and dataset. Build/run both the same way (route,
toolchain, flags, user and restrictions); only code differs. Start each
run only at a 1-minute load below 4; exclude warm-up posts. Absolute values
from another session never decide a result alone.

### Build routes

These commands are for the approved measurement operator, outside the
implementer's sandbox. Use trusted git/Go installations and a trusted
Docker Go image pinned by digest. Set `sha` to the full approved head or
fixed trusted base SHA; repeat in a new checkout for the other. No local
clone, config, hooks or cache is reused. Hardening follows the
[trusted invocation](running-argon.md#the-trusted-invocation), with hooks
and fsmonitor also disabled in the fresh repository:

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
`image` is the trusted image's digest reference. Module downloads are
checked against this SHA's `go.sum`. Mount only source (read-only) and the
empty output directory; no host home, credentials or Docker socket:

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

Use the actual host architecture for both SHAs. Do not regenerate assets
or execute any source-tree command other than this Go build. Record SHA
and binary checksum (`shasum -a 256`) before staging; `-buildvcs=false`
avoids container git ownership differences. No PR binary runs there.

If this cannot produce a usable binary, record why. The maintainer stages
the freshly verified source read-only under the dedicated build directory,
empty of prior output/caches; no symlink may escape it. The dedicated user
can write only its build output/caches there. Recheck staged `HEAD` and
clean status with `pinned` (set `src` to the staged source) before building.
Set `go` to the absolute trusted Go executable; use the same version and
flags as the container:

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

The maintainer does this setup, not the implementing AI:

- Create `_ribbitto_measure`, a non-admin user with no login, credentials,
  privileged groups or access to Docker, developer homes or shared data.
- Create an administrator-owned staging directory at
  `/private/var/ribbitto-measure`, with a read-only `server` binary slot
  and a user-writable `data` directory. Keep the fallback `build` directory
  separate; the server profile cannot read it. Use real paths, no symlinks.
- Install the profile below, administrator-owned and not writable by the
  measurement user, at `/private/var/ribbitto-measure/server.sb`. Reserve
  server port 18080 and throwaway database port 55434; neither may belong
  to another service. Port changes update profile and commands together,
  never allow general loopback access.
- Validate on this macOS version with the trusted base: startup and a
  database-backed request must work; attempts to read outside the allowed
  paths, execute another program, bind another address/port or connect to
  other ports (including shared PostgreSQL and Chrome) must fail. Record
  the validation and profile in the PR. A denial is a stop, not permission
  to widen the profile without review and maintainer approval.

Proposed minimal profile (system libraries and devices are runtime reads,
not access to developer files; no broad profile imports):

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
(allow network-bind (local tcp "127.0.0.1:18080"))
(allow network-inbound (local tcp "127.0.0.1:18080"))
(allow network-outbound (remote tcp "127.0.0.1:55434"))
```

For each SHA the operator stages its checksummed binary in the read-only
slot while no server is running, clears `data`, and restores the same
dataset using only trusted base tools. Start the throwaway database at
`127.0.0.1:55434`; set `db_url` to its disposable connection string with
that numeric host/port and no fallback hosts or Unix sockets. Run in the
foreground, with no inherited open files except standard input/output/error:

```sh
cd /private/var/ribbitto-measure/data &&
  sudo -u _ribbitto_measure /usr/bin/env -i PATH=/usr/bin:/bin \
  HOME=/private/var/ribbitto-measure/data TMPDIR=/private/var/ribbitto-measure/data \
  TZ=UTC RIBBITTO_ADDR=127.0.0.1:18080 RIBBITTO_DATABASE_URL="$db_url" \
  /usr/bin/sandbox-exec -f /private/var/ribbitto-measure/server.sb \
  /private/var/ribbitto-measure/server serve < /dev/null
```

The trusted harness/Chrome run separately as the operator, never as the
server user and never with their DevTools port allowed to the server.
Stop the foreground server before swapping binaries; apply condition 9
after the session, including on error. Until this setup and validation
exist, condition 2's per-SHA maintainer approval is still required.
