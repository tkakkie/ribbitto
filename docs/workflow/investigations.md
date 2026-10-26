# Investigations and the performance target

- New measurement/verification stages, prototypes and measurement
infrastructure need explicit maintainer preapproval.
- Investigation/optimisation issues state a PR/time cap before work;
stop and report at cap, even inconclusive.
- Target: 5,000 concurrent connections/process; message arrival not much
worse than without unread counts. Check 2 vCPU / 4 GB in its own issue
when available; development-machine runs decide until then.

## Host measurements

Approved performance measurements are the only standing exception to the
[handoff rule](running-other-ai.md#handoff-of-another-ais-code).
After handoff, builds, tests and `make dev` stay in CI or sandboxes.
All conditions apply:

1. Cross-review (Claude or Codex), Copilot and adversarial review approve
with measurement pending; CI passes at that head before `ai-reviewed`.
Measure after that label. Before asking for merge, the orchestrator
checks results against the issue's *Done when*.
2. Match the full build/run SHA to reviews and CI; a label is not enough.
Each push needs reviews and CI at its SHA before another run. Until setup
is validated, each run also needs case-by-case maintainer approval for
that SHA.
3. The measurement is required by the issue's *Done when*, approved by
`ready`, and within the issue's cap.
4. Only the PR's `cmd/ribbitto` server runs. Seed, migrations, harness and
other tools use the trusted, fixed base SHA or a source approved in
advance. A head adding a migration is a stop
for the maintainer.
5. First cross-compile in isolation; only the macOS binary reaches the host.
Both routes build both approved full SHAs from fresh, verified clean
checkouts with the [trusted git settings and `GOENV=off`](running-argon.md#the-trusted-invocation).
Never build from the implementer's worktree or sandbox: ignored/untracked
files and local git config can alter builds at the same `HEAD`. Embed
assets; run time needs no head tree.
6. If isolation is impossible, build as the dedicated low-privilege
user with the minimal environment below. Use the same route for base.
7. Run as that user under the file/network profile; `env -i` restricts
neither. Use only its binary/data/runtime paths and server/database
loopback ports; shared databases and Chrome's DevTools stay denied. The
maintainer creates the user and installs/validates the
profile once. If restrictions fail, keep them; stop for a case-by-case
maintainer decision.
8. Listen only on `127.0.0.1`; publish a throwaway database container only
on its chosen `127.0.0.1` port, with disposable data and credentials.
9. Afterwards, even on failure, stop all servers; remove database/container,
volume, checkouts/worktrees, caches and binaries. Keep only setup/results.
10. In PR, record that it was a host measurement, SHAs, conditions (toolchain,
image digest, restrictions), exact commands and results. Redact credentials.

**Reproducibility.** Alternate base/head in one session: same
host, Chrome, profile, dataset, build/run route, toolchain, flags, user,
restrictions; only code differs. Start at 1-min load below 4; exclude
warm-up posts. Another session's absolute values alone never decide.

### Build routes

Both routes run Go webbuild in the same isolation/trusted environment,
outside Argon verify. Keep verified `src` read-only; generate in a writable
copy, natively (container linux/arm64, user darwin/arm64), then cross-compile.
Record the diff listing in PR; only `web/static/dist` may differ.

Approved operator: trusted git/Go; `image`: trusted Docker Go image digest
([current value](measurement-go-image.md#measurement-go-image)).
Apply condition 5; no clone/config/hooks/cache reuse. `sha`: full approved
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

On success, cross-compile in a throwaway container; check modules against this SHA's `go.sum`.
After HEAD/clean verification, both routes use:

```sh
build_steps='
! command -v node || exit 1
cp -R "$1" gen; chmod -R u+w gen; cd gen
"$4" -C tools run -mod=readonly -buildvcs=false ./webbuild .. "$3" measurement message-time
d=$(diff -qr "$1" .) && exit 1
test "$d" = "Only in ./web/static: dist"; printf "%s\n" "$d"
GOOS=darwin GOARCH=arm64 "$4" build -mod=readonly -trimpath -buildvcs=false -o "$2/ribbitto" ./cmd/ribbitto
'
```

Mount only read-only source and empty output:

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

Apple silicon only, never Rosetta. In-tree: only the webbuild step above and this Go build.
Before staging record SHA and `shasum -a 256`.

If unusable, record why. Maintainer: stage read-only `build/src`,
no old output/caches or escaping symlinks. User writes only empty
`work`/`out`/`cache` under `build`. Recheck staged `src` `HEAD`/clean status via `pinned`;
require `test -z "$(find "$src" -type l)"`.
`go`: absolute trusted Go path, container version/flags:

```sh
b=/private/var/ribbitto-measure/build && cd "$b/work" &&
  sudo -u _ribbitto_measure /usr/bin/env -i PATH=/usr/bin:/bin \
  HOME="$b/cache" GOENV=off GOTOOLCHAIN=local \
  GOCACHE="$b/cache/go-build" GOMODCACHE="$b/cache/mod" \
  GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_NO_REPLACE_OBJECTS=1 \
  CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 \
  /bin/sh -eu -c "$build_steps" build "$b/src" "$b/out" "$sha" "$go"
```

### One-time setup and run

Maintainer-only setup:

- Create non-admin `_ribbitto_measure`: no login, credentials, privileged
groups or access to Docker, developer homes or shared data.
- Create administrator-owned `/private/var/ribbitto-measure`: read-only
`server` slot, user-writable `data`, separate fallback `build` unreadable
by the server profile. Real paths, no symlinks.
- Install the administrator-owned profile below at
`/private/var/ribbitto-measure/server.sb`, not writable by measurement
user. Reserve ports 18080 (server) and 55434 (database) exclusively.
Change ports in profile and commands together; never allow all loopback.
- Validate this macOS version in order as dedicated user with
trusted base. Watch denials:
`log stream --style compact --predicate 'sender == "Sandbox"'`:
  1. No database; use run user/environment:
`sandbox-exec -f /private/var/ribbitto-measure/server.sb /private/var/ribbitto-measure/server bogus`.
Expect logged usage error, exit 1: profile, dyld and Go start.
  2. With throwaway DB, send a DB-backed request to `127.0.0.1:18080`.
Prefer `db_url`
with explicit `user=` and `sslmode=disable` to allowing `mach-lookup`.
  3. Test copy: add only execution/runtime reads for `/bin/cat` and
`/usr/bin/nc`; same user/environment and other file/network rules.
Confirm denial: outside reads (including operator's `~`), connections to
5432/55433/Chrome's DevTools port, other execution and address/port binds.
Record results/profiles in PR. A denial stops; widening needs review and
maintainer approval.

Minimal profile: system runtime reads exclude developer files; no broad
imports. SBPL needs `localhost` (loopback only), not numeric hosts;
commands still use `127.0.0.1`:

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
(allow file-read* (literal "/") (literal "/dev/null") (literal "/dev/urandom"))
(allow network-bind (local tcp "localhost:18080"))
(allow network-inbound (local tcp "localhost:18080"))
(allow network-outbound (remote tcp "localhost:55434"))
```

Per SHA: stop servers; stage checksummed binary read-only; clear `data`;
restore dataset with trusted base tools only. Start DB; `db_url`: disposable,
`127.0.0.1:55434`, explicit `user=`, `sslmode=disable`, no fallback hosts/Unix
sockets. Run in foreground.
Trusted Perl forks into a new session, replaces fds 0-2 with `/dev/null` and
fresh `data/server.log`, and closes higher fds (directory handle included)
before the profile.
Ctrl-C then stops only the waiting parent; stop the server by PID.

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

Administrator after start: `lsof -a -p "$pid" -d 0-65535` (`pid`: server).
Require 0: `/dev/null`; 1-2: fresh log; higher: server-opened allowed files,
runtime kqueue/pipes or permitted sockets only. Stop on inherited operator
files/terminals/sockets.

Trusted harness/Chrome run separately as operator, never as server user.
