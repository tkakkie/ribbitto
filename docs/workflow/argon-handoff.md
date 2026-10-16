# Argon handoff check

How Claude checks an Argon trial change ([trial](argon-implementer.md))
without running anything Argon wrote with Claude's own rights. Argon's code
runs only in a copy of the tree, under macOS Seatbelt (`/usr/bin/sandbox-exec`)
with the profile [`argon-handoff.sb`](argon-handoff.sb) from `origin/main`,
in an empty environment. Nothing from that copy flows back.

## What the sandbox enforces

The profile applies to every child process: a nested `sandbox-exec` cannot
loosen it, and setuid programs (`ps`, `at`, `crontab`) cannot run. Checked
on macOS 27.0.1 on 2026-10-16 with harmless probes (shell commands and a Go
test, no model involved), and by running the procedure below on a sample
change with a planted ignored `bin/templ`, which never ran. Each of these was refused or had no effect:

- reads of a sentinel file in the account's home, of `~/.config/gh` and
  `~/.ssh`, and of `/private/tmp` and `/private/var/folders`;
- writes outside the run directory, `/private/var/tmp` included;
- a connection to an outside address (`1.1.1.1:443`) and to a Unix socket;
- the keychain (`security`), Launch Services (`open`), Apple events
  (`osascript`) and a `launchctl submit` job.

Connections to `127.0.0.1` worked, so the test database and the tests' own
servers do. Seatbelt can only allow loopback or every host, so nothing
outside is reachable. Modules and the pinned tools are therefore fetched
before the run, from the base commit's code.

Within that, the full `make check` passes in the sandbox except in four
places, and none of them is a security relaxation:

- the process tests that need `ps`, in `cmd/argonreview`, `cmd/capacity` and
  `cmd/dev` (as in Codex's sandbox, #183);
- `cmd/loadgen`'s `TestSourceDials`, which binds `127.0.0.2`;
- the browser scenarios, which skip because Chrome cannot start in the
  sandbox.

CI runs all of them with read-only access and no secrets. A trial change
that touches any of these packages cannot be checked here, so it leaves the
trial. Any other failure is a real failure.

## The procedure

`W` is Argon's worktree and `B` its base commit. Run the steps in order, in
zsh, from a directory outside `W` and the run directory (`lsof` in step 6
must not find Claude's own shell). If any step fails, stop.

1. **Argon has stopped.** `lsof +D "$W"` lists no process. Repeat the
   pre-run checks from [the boundary](argon-implementer.md#the-boundary),
   and move `PR_BODY.md` out of `W`.
2. **The change as a patch,** made by git with hooks, fsmonitor and
   configuration off:

   ```sh
   g() { env -i PATH=/usr/bin:/bin HOME=/nonexistent GIT_CONFIG_GLOBAL=/dev/null \
     GIT_CONFIG_NOSYSTEM=1 git -C "$W" -c core.hooksPath=/dev/null -c core.fsmonitor=false "$@"; }
   g add -A && g diff --cached --binary "$B" > "$patch"
   ```

   Stop if the patch adds a symlink or submodule (mode `120000` or
   `160000`), or touches `.git`, `.agents`, `.gemini`, `.claude`,
   `AGENTS.md`, `CLAUDE.md`, `GEMINI.md`, `bin/` or a package from the list
   above. Ignored files, such as a planted `bin/templ`, are never in it.
3. **A fresh copy of the base.** The run directory is a new directory
   directly under `/private/var/tmp/ribbitto-handoff` (mode 700), holding
   `src`, `home`, `tmp` and `cache`:

   ```sh
   mkdir -p -m 700 /private/var/tmp/ribbitto-handoff &&
     T=$(mktemp -d /private/var/tmp/ribbitto-handoff/run.XXXXXX) &&
     mkdir "$T"/{src,home,tmp,cache} &&
     g archive "$B" | tar -x -C "$T/src" &&
     g show origin/main:docs/workflow/argon-handoff.sb > "$T.sb"
   ```

   In `src`, a throwaway repository (`git init`, `gc.auto 0`, one commit)
   gives the tests that need one a repository; the temporary directory sits
   outside it. The profile stays outside `$T`, where nothing in the run can
   write.
4. **Downloads, from the base's code, outside the sandbox.** With
   `GOROOT=$(cd "$T/src" && go env GOROOT)` (the base's Go version) and
   `e=(env -i PATH="$GOROOT/bin:/usr/bin:/bin" HOME="$T/home" TMPDIR="$T/tmp"
   GOTOOLCHAIN=local GOCACHE="$T/cache/go-build" GOMODCACHE="$T/cache/mod"
   GOPATH="$T/cache/gopath" GOLANGCI_LINT_CACHE="$T/cache/lint")`, run
   `go mod download` and `go -C tools mod download`, then `make` the three
   pinned tools (`./bin/templ` and the two paths `go -C tools run ./download
   -paths` prints). Then `git apply --index "$patch"`. If it changed
   `go.mod` or `go.sum`, run `go mod download` once more; it only downloads,
   checked against `go.sum`, and fails on a Go version other than the
   base's (`GOTOOLCHAIN=local`).
5. **The check, in the sandbox.** For each of `check-static`, `check-test`
   and `check-tools`, from `$T/src`:

   ```sh
   "${e[@]}" GOPROXY=off GOFLAGS=-mod=readonly RIBBITTO_REQUIRE_DB=1 \
     RIBBITTO_TEST_DATABASE_URL='<local test database>' \
     /usr/bin/sandbox-exec -D TREE="$T" -D GOROOT="$GOROOT" -f "$T.sb" make <target>
   ```

   Only the failures listed above are expected.
6. **Clean-up.** `lsof -t +D "$T"` lists processes the run left behind; stop
   each by PID, then remove `$T` (after `chmod -R u+w "$T"`, since the
   module cache is read-only) and `$T.sb`. A left-behind process that
   moved its working directory out of `$T` is not listed. It stays
   sandboxed, with loopback only and nowhere left to write. Compare the
   account's processes started since step 5, and tell the maintainer about
   any unexplained one rather than stopping it.
7. **Before every commit and push**, repeat the pre-run checks on `W`: no
   symlink, no workspace configuration at any depth, the tracked `AGENTS.md`
   and `CLAUDE.md` as on `origin/main`, and `W/.git` still the one line
   pointing at the main checkout's worktree entry. Then commit and push with
   `-c core.hooksPath=/dev/null -c core.fsmonitor=false`.

## In the preflight

Besides the probes in [the preflight](argon-implementer.md#preflight), this
procedure runs once on a throwaway repository with a planted ignored
`bin/templ` and a test that tries each probe above, plus the environment
sentinel and a background process. The binary never runs, each probe is
refused or has no effect, and the process is found and stopped. The results
are recorded on #1206. If the sandbox cannot be shown to hold, the trial
stops.
