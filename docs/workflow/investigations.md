# Investigations and the performance target

- New measurement/verification stages, prototypes and infrastructure need
explicit maintainer preapproval.
- Investigation/optimisation issues need a PR/time cap before work;
stop/report at cap, even inconclusive.
- Target: 5,000 concurrent connections/process; message arrival not much
worse than without unread counts. Check 2 vCPU / 4 GB in its own issue
when available; local runs decide meanwhile.

## Host measurements

Approved performance measurements alone have a standing exception to
[handoff rule](running-other-ai.md#handoff-of-another-ais-code) (maintainer, 2026-10-18, #1308).
After handoff, builds/tests/`make dev` stay in CI or sandboxes. Conditions:

1. Cross-review (Claude or Codex), Copilot and adversarial review approve
with measurement pending; CI passes at that head before `ai-reviewed`. Measure
after that label. Before merge, the orchestrator checks results against
*Done when*.
2. Match the full build/run SHA to reviews and CI; a label is not enough.
Each push needs reviews and CI at its SHA before rerunning. Until setup
is validated, each run also needs case-by-case maintainer approval for
that SHA (#1273, #1276).
3. Measurement must be in *Done when*, approved by `ready`, and within the issue's cap.
4. Only the PR's `cmd/ribbitto` server runs. Seed, migrations, harness and
other tools use the trusted, fixed base SHA or a preapproved source (e.g.
external harness). A new migration stops for the maintainer.
5. First cross-compile in isolation; only the macOS binary reaches the host.
Both routes build approved full SHAs from fresh verified clean checkouts
with the [trusted git settings and `GOENV=off`](running-argon.md#the-trusted-invocation).
Never build from the implementer's tree/sandbox: ignored/untracked files
and local git config can alter builds at the same `HEAD`. Embed
assets; run time needs no head tree.
6. If isolation is impossible, build as the dedicated low-privilege user with the
minimal environment below. Use the same route for base.
7. Run as that user under the file/network profile; `env -i` restricts
neither. Use only its binary/data/runtime paths and server/database
loopback ports; shared databases and Chrome's DevTools stay denied. Maintainer
creates the user and installs/validates the profile once. If restrictions
fail, keep them; stop for the maintainer's case-by-case decision.
8. Listen only on `127.0.0.1`; publish a throwaway database container only
on its chosen `127.0.0.1` port, with disposable data and credentials.
9. Even on failure, stop servers; remove database/container, volume,
checkouts/worktrees, caches and binaries. Keep only setup/results.
10. PR records host measurement, SHAs, toolchain, image digest, restrictions,
exact commands and results. Redact credentials.

**Reproducibility (#1149).** Alternate base/head in one session: same
host, Chrome, profile, dataset, build/run route, toolchain, flags, user,
restrictions; only code differs. Start at 1-min load below 4; exclude warm-ups.
Another session's absolute values never decide alone.

### Build routes

Generate before compiling in the same isolated build, outside Argon verify:
Go only, no Node on `PATH`, same trusted checkouts/flags. Copy verified
read-only `src` to writable `generated`. The builder uses `tools/go.sum` on
native linux/arm64 (container) or darwin/arm64 (dedicated user), then
cross-compile. Record diff in PR: only `web/static/dist` differs.

Trusted git/Go; `image`: [trusted Docker Go digest](measurement-go-image.md#measurement-go-image).
Condition 5: no clone/config/hooks/cache reuse. `sha`: full approved
head/fixed trusted base SHA. Disable hooks/fsmonitor; refuse symlinks:

```sh
session=$(mktemp -d /private/tmp/ribbitto-measure.XXXXXX) &&
  src=$session/src && mkdir "$src" "$session/out" &&
  pinned() { env -i PATH="$PATH" GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_NO_REPLACE_OBJECTS=1 git -C "$src" -c core.hooksPath=/dev/null -c core.fsmonitor=false -c core.symlinks=false "$@"; } &&
  pinned init --quiet --template= &&
  pinned fetch --quiet https://github.com/tkakkie/ribbitto.git "$sha" &&
  replaced=$(pinned for-each-ref refs/replace) && test -z "$replaced" &&
  tree=$(pinned ls-tree -r --full-tree "$sha") &&
  printf '%s\n' "$tree" | awk '$1 == "120000" { exit 1 }' &&
  pinned checkout --quiet --detach "$sha" &&
  test "$(pinned rev-parse HEAD)" = "$sha" &&
  tree_status=$(pinned status --porcelain --ignored --untracked-files=all) &&
  test -z "$tree_status"
```

After HEAD/clean verification, both routes use:

```sh
build_steps='
  if command -v node; then exit 1; fi
  cp -R "$1" generated
  chmod -R u+w generated
  cd generated
  "$4" -C tools run -mod=readonly -buildvcs=false ./webbuild .. "$3" measurement message-time
  changes=$(diff -qr "$1" .) && exit 1
  test "$changes" = "Only in ./web/static: dist"
  printf "%s\n" "$changes"
  GOOS=darwin GOARCH=arm64 "$4" build -mod=readonly -trimpath -buildvcs=false -o "$2/ribbitto" ./cmd/ribbitto
'
```

Container: read-only source, empty output:

```sh
docker run --rm --read-only --cap-drop=ALL --security-opt=no-new-privileges \
  --user "$(id -u):$(id -g)" --tmpfs /tmp:exec,mode=1777 \
  --mount "type=bind,src=$src,dst=/src,readonly" \
  --mount "type=bind,src=$session/out,dst=/out" --workdir /tmp \
  --entrypoint /usr/bin/env "$image" -i PATH=/usr/local/go/bin:/usr/bin:/bin \
  HOME=/tmp GOENV=off GOTOOLCHAIN=local GOCACHE=/tmp/build GOMODCACHE=/tmp/mod \
  GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_NO_REPLACE_OBJECTS=1 \
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  /bin/sh -eu -c "$build_steps" build /src /out "$sha" /usr/local/go/bin/go
```

Apple silicon, never Rosetta. In-tree: only generation and this Go build.
Before staging record SHA, `shasum -a 256`;
`-buildvcs=false` avoids container git ownership mismatches.

If unusable, record why. Stage read-only `build/src` as maintainer; no old
output/caches or escaping symlinks. User writes only empty `work`/`out`/`cache`
under `build`. Recheck staged `src` HEAD/clean via `pinned`;
require `test -z "$(find "$src" -type l)"`.
`go`: absolute trusted Go path, container version/flags:

```sh
build=/private/var/ribbitto-measure/build && cd "$build/work" &&
  sudo -u _ribbitto_measure /usr/bin/env -i PATH=/usr/bin:/bin \
  HOME="$build/cache" GOENV=off GOTOOLCHAIN=local \
  GOCACHE="$build/cache/go-build" \
  GOMODCACHE="$build/cache/mod" \
  GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_NO_REPLACE_OBJECTS=1 \
  CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
  /bin/sh -eu -c "$build_steps" build "$build/src" \
  "$build/out" "$sha" "$go"
```

### One-time setup and run

Maintainer-only setup:

- Create non-admin `_ribbitto_measure`: no login, credentials, privileged
groups, Docker, developer homes or shared data.
- Create administrator-owned `/private/var/ribbitto-measure`: read-only
`server` slot, user-writable `data`, separate fallback `build` unreadable
by the server profile. Real paths, no symlinks.
- Install the administrator-owned profile below at
`/private/var/ribbitto-measure/server.sb`, not writable by measurement
user. Reserve ports 18080 (server) and 55434 (database) exclusively.
Change ports in profile and commands together; never allow all loopback.
- Validate this macOS version in order as dedicated user with trusted base. Denials:
`log stream --style compact --predicate 'sender == "Sandbox"'`:
  1. No database; use run user/environment:
`sandbox-exec -f /private/var/ribbitto-measure/server.sb /private/var/ribbitto-measure/server bogus`.
Expect logged usage error, exit 1: profile, dyld and Go start.
  2. With throwaway DB, send a DB-backed request to `127.0.0.1:18080`.
Possible gaps: self-signals (`(allow signal (target self))`),
parent-directory metadata reads, pgx current-user lookup. Prefer `db_url`
with explicit `user=` and `sslmode=disable` to allowing `mach-lookup`.
  3. Test copy: add only execution/runtime reads for `/bin/cat` and
`/usr/bin/nc`; same user/environment and other file/network rules.
Confirm denied: outside reads (including operator's `~`), connections to
5432/55433/Chrome's DevTools port, other execution and address/port binds.
Record results/profiles in PR. Stop on denial; widening needs review and
maintainer approval.

Profile: runtime reads exclude developer files; no broad imports.
SBPL needs `localhost` (loopback only); commands use `127.0.0.1`:

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

Per SHA: stop servers; stage checksummed binary read-only; clear `data`;
restore via trusted base tools only. Start DB; `db_url`: disposable,
`127.0.0.1:55434`, explicit `user=`, `sslmode=disable`, no fallback hosts/Unix
sockets. Foreground only.
Trusted Perl starts a new session, replaces fds 0-2 with `/dev/null` and
fresh `data/server.log`, closes higher fds (directory handle included)
before the profile; `env -i` leaves files open.
Ctrl-C stops only the waiting parent; stop the server by PID.

```sh
cd /private/var/ribbitto-measure/data &&
  sudo -u _ribbitto_measure /usr/bin/env -i PATH=/usr/bin:/bin \
  HOME=/private/var/ribbitto-measure/data TMPDIR=/private/var/ribbitto-measure/data \
  TZ=UTC RIBBITTO_ADDR=127.0.0.1:18080 RIBBITTO_DATABASE_URL="$db_url" \
  /usr/bin/perl -MPOSIX -MFcntl=:DEFAULT -e '
  my $pid = fork(); defined $pid or die $!;
  if ($pid) {
    waitpid($pid, 0) == $pid or die $!;
    exit(($? & 127) ? 128 + ($? & 127) : $? >> 8);
  }
  POSIX::setsid() > 0 or die $!;
  open(STDIN, "<", "/dev/null") or die $!;
  sysopen(STDOUT, "server.log", O_WRONLY|O_CREAT|O_EXCL|O_NOFOLLOW, 0600) or die $!;
  open(STDERR, ">&", \*STDOUT) or die $!;
  opendir(my $d, "/dev/fd") or die $!;
  my @fds = readdir $d; closedir($d) or die $!;
  for (@fds) {
    next unless /^\d+$/ && $_ > 2;
    POSIX::close($_) or $!{EBADF} or die $!;
  }
  exec @ARGV; die $!;' \
  /usr/bin/sandbox-exec -f /private/var/ribbitto-measure/server.sb \
  /private/var/ribbitto-measure/server serve
```

Administrator: `lsof -a -p "$pid" -d 0-65535` (`pid`: server).
Require 0: `/dev/null`; 1-2: fresh log; higher: server-opened allowed files,
runtime kqueue/pipes or permitted sockets only. Stop on inherited operator
files/terminals/sockets.

Trusted harness/Chrome: operator, never server user.
