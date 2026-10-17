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
After handoff, builds, tests and `make dev` stay in CI or sandboxes.
All conditions apply:

1. Cross-review (Claude or Codex), Copilot and adversarial review approve
   with measurement pending; CI passes at that head before `ai-reviewed`.
   Measure after that label. Before asking for merge, the orchestrator
   checks results against the issue's *Done when*.
2. Match the full build/run SHA to reviews and CI; a label is not enough.
   Each push needs reviews and CI at its SHA before another run. Until setup
   is validated, each run also needs case-by-case maintainer approval for
   that SHA (#1273, #1276).
3. The measurement is required by the issue's *Done when*, approved by
   `ready`, and within the issue's cap.
4. Only the PR's `cmd/ribbitto` server runs. Seed, migrations, harness and
   other tools use the trusted, fixed base SHA or a source approved in
   advance (e.g. an external harness). A head adding a migration is a stop
   for the maintainer.
5. First cross-compile in isolation; only the macOS binary reaches the host.
   Both routes build both approved full SHAs from fresh, verified clean
   checkouts with the [trusted git settings and `GOENV=off`](running-argon.md#the-trusted-invocation).
   Never build from the implementer's worktree or sandbox: ignored/untracked
   files and local git config can alter builds at the same `HEAD`. Embed
   assets; no head tree is needed at run time.
6. If an isolated build is impossible, build as the dedicated low-privilege
   user with the minimal environment below. Use the same route for base.
7. Run as that user under the file/network profile; `env -i` restricts
   neither. Use only its binary/data/runtime paths and server/database
   loopback ports; shared databases and Chrome's DevTools stay denied. The
   maintainer creates the user (a system change) and installs/validates the
   profile once. If restrictions fail, keep them; stop for a case-by-case
   maintainer decision.
8. Listen only on `127.0.0.1`; publish a throwaway database container only
   on its chosen `127.0.0.1` port, with disposable data and credentials.
9. Afterwards, even on failure, stop all servers; remove database/container,
   volume, checkouts/worktrees, caches and binaries. Keep only setup/results.
10. Record SHAs, conditions (toolchain, image digest, restrictions), exact
    commands, results and that this was a host measurement in the PR.
    Redact credentials.

**Reproducibility (#1149).** Alternate base/head in one session with the
same host, Chrome, profile, dataset, build/run route, toolchain, flags, user
and restrictions; only code differs. Start at 1-minute load below 4; exclude
warm-up posts. Another session's absolute values never decide results alone.

### Build routes

The approved operator works outside the implementer's sandbox with trusted
git/Go and a trusted Docker Go image pinned by digest. Set `sha` to the full
approved head or fixed trusted base SHA; repeat in a fresh checkout. Reuse
no local clone, config, hooks or cache. Apply condition 5's git settings;
also disable hooks/fsmonitor:

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

Continue only on success. Cross-compile in a throwaway container; `image`
is the trusted image's digest reference. Check modules against this SHA's
`go.sum`. Mount only read-only source and empty output; no host home,
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

Apple silicon only: both SHAs use native arm64, never Rosetta. Run only
this Go build in the tree: no asset regeneration or PR binaries. Record SHA
and `shasum -a 256` before staging;
`-buildvcs=false` avoids container git ownership differences.

If unusable, record why. The maintainer stages freshly verified source
read-only in dedicated `build`, empty of old output/caches; no symlink may
escape. The user writes only output/caches there. Recheck staged `HEAD` and
clean status with `pinned` (`src` is staged source). Set `go` to the absolute
trusted Go executable; match the container's version and flags:

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

Setup is maintainer-only:

- Create `_ribbitto_measure`, a non-admin user with no login, credentials,
  privileged groups or access to Docker, developer homes or shared data.
- Create administrator-owned `/private/var/ribbitto-measure`: read-only
  `server` binary slot, user-writable `data`, separate fallback `build`
  unreadable by the server profile. Use real paths, no symlinks.
- Install the profile below, administrator-owned and not writable by the
  measurement user, at `/private/var/ribbitto-measure/server.sb`. Reserve
  server port 18080 and database port 55434 for these alone.
  Change ports in profile and commands together; never allow all loopback.
- Validate this macOS version as the dedicated user with the trusted base,
  in order. Watch denials with
  `log stream --style compact --predicate 'sender == "Sandbox"'`:
  1. With no database, use the run command's user/environment below:
     `sandbox-exec -f /private/var/ribbitto-measure/server.sb /private/var/ribbitto-measure/server bogus`.
     Expect logged usage error and exit 1: profile, dyld and Go start.
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
  Record results/profiles in the PR. A denial is a stop; widening needs
  review and maintainer approval.

Minimal profile: system runtime reads exclude developer files; no broad
imports. SBPL requires `localhost`, not numeric hosts; it matches only
loopback. Commands still use `127.0.0.1`:

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

For each SHA, stop servers, stage its checksummed binary read-only, clear
`data` and restore the same dataset with trusted base tools only.
Start the throwaway database at `127.0.0.1:55434`; set `db_url` to its
disposable connection string with that numeric host/port, explicit `user=`
and `sslmode=disable`, no fallback hosts or Unix sockets. Run in the
foreground. Trusted Perl closes all descriptors above 2 as that user before the
profile, including its directory handle; only standard input/output/error
survive. `env -i` alone leaves files open:

```sh
cd /private/var/ribbitto-measure/data &&
  sudo -u _ribbitto_measure /usr/bin/env -i PATH=/usr/bin:/bin \
  HOME=/private/var/ribbitto-measure/data TMPDIR=/private/var/ribbitto-measure/data \
  TZ=UTC RIBBITTO_ADDR=127.0.0.1:18080 RIBBITTO_DATABASE_URL="$db_url" \
  /usr/bin/perl -MPOSIX -e 'opendir(my $d, "/dev/fd") or die $!; my @fds = readdir $d; closedir($d) or die $!; for (@fds) { next unless /^\d+$/ && $_ > 2; POSIX::close($_) == 0 or $!{EBADF} or die $!; } exec @ARGV; die $!;' \
  /usr/bin/sandbox-exec -f /private/var/ribbitto-measure/server.sb \
  /private/var/ribbitto-measure/server serve < /dev/null
```

As administrator check `lsof -a -p "$pid" -d 3-65535` after start (`pid` is the
server PID). Stop on inherited operator files/sockets; record in the PR.

Trusted harness/Chrome run separately as operator, never as server user;
keep DevTools denied. Stop the server before swapping binaries; apply
condition 9 after the session, even on error.
