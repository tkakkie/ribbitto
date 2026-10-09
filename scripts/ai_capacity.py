"""Run outside the sandbox: python3 scripts/ai_capacity.py WT1 WT2 PACKAGES PARALLEL."""
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import tempfile
import time
import ai_env


def session_members(session):
    """Return the PIDs whose session is session, or None if ps fails.

    Each check starts its own session. A descendant that moves to another
    process group (Chromium, started by the browser test, does) or loses its
    parent stays in that session, so the session finds it where parent PIDs
    or the group would not. macOS ps prints no session IDs, so getsid asks.
    Accepted limit (maintainer, #636): a descendant that calls setsid() leaves
    the session and is not found; the browser test's Chromium does not.
    """
    try:
        listing = subprocess.run(["ps", "-axo", "pid="], capture_output=True, text=True,
                                 timeout=10, check=True).stdout
    except (OSError, subprocess.SubprocessError):
        return None
    members = []
    for field in listing.split():
        pid = int(field)
        try:
            if pid != os.getpid() and os.getsid(pid) == session:
                members.append(pid)
        except (ProcessLookupError, PermissionError):
            pass
    return members


def kill_member(pid, session):
    # Check the session again just before the signal. macOS has no pidfd, so a
    # member that exits and whose PID is reused in the instant between this
    # check and the kill cannot be told apart: SIGKILL may then, rarely, reach
    # an unrelated process. PIDs are allocated sequentially, which makes
    # immediate reuse unlikely, and the check keeps the window to these two
    # calls. The maintainer accepted this limit for this development-only
    # runner (#636); do not add more PID management here.
    try:
        if os.getsid(pid) == session:
            os.kill(pid, signal.SIGKILL)
    except (ProcessLookupError, PermissionError):
        pass


def stop_group(worker):
    # The make parent may already have exited while a child is still alive.
    # Kill the check's group, then every process left in its session, until
    # none remains: a member can fork before it dies, and its child inherits
    # the session. Returns whether the session was seen empty; if ps fails,
    # members outside the group may survive, so the caller must fail the run.
    session = worker.pid
    # Only a leader that has not been reaped still owns its PID; once poll or
    # wait has reaped it, killpg could reach a new group that reused the PID.
    if worker.poll() is None:
        try:
            os.killpg(session, signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            pass
    for _ in range(100):
        members = session_members(session)
        if not members:
            break
        for pid in members:
            kill_member(pid, session)
        time.sleep(0.02)
    worker.wait()
    return members == []

def measure(roots, packages, parallel):
    cpus = len(os.sched_getaffinity(0)) if hasattr(os, "sched_getaffinity") else os.cpu_count()
    report = dict(packages=packages, parallel=parallel, count=1, require_db=1,
                  # pgtest's admin connection is plain pgx, which would send a
                  # pool_max_conns URL key to the server, so pools keep pgx's default.
                  pgx_pool_max_conns=max(4, cpus),
                  migration_pool_max_open=0, admin_connections_per_pgtest_call=1)
    workers, outputs, stopped, unconfirmed = [], [], set(), []

    def stop_once(worker):
        if worker.pid not in stopped:
            stopped.add(worker.pid)
            if not stop_group(worker):
                unconfirmed.append(worker.pid)

    observer = subprocess.Popen([str(ai_env.HELPER), "--observe"], stdin=subprocess.PIPE,
                                stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
    try:
        # The persistent connection must be ready before any check takes a slot.
        report.update(json.loads(observer.stdout.readline()))
        for root in roots:
            environment = os.environ.copy()
            environment["RIBBITTO_REQUIRE_DB"] = "1"
            for name, suffix in (("GOCACHE", "go-build"), ("GOLANGCI_LINT_CACHE", "lint")):
                environment[name] = str(root / "bin/.cache" / suffix)
            # Reuse inherited/default GOMODCACHE and GOPATH, including downloaded tools.
            (root / "bin").mkdir(exist_ok=True)
            fd, name = tempfile.mkstemp(prefix="ai-capacity-", suffix=".log", dir=root / "bin")
            outputs.append(Path(name))
            with os.fdopen(fd, "w") as output:
                workers.append(subprocess.Popen(["make", "check", f"GO_TEST_FLAGS=-count=1 -p {packages} -parallel {parallel}"],
                                                cwd=root, env=environment, stdout=output,
                                                stderr=subprocess.STDOUT, start_new_session=True))
        while True:
            # Poll every check (any() would stop at the first running one) and
            # clean up a finished check at once, not when the slower one ends:
            # its session's PID stays reserved only while members remain.
            running = [worker for worker in workers if worker.poll() is None]
            for worker in workers:
                if worker.returncode is not None:
                    stop_once(worker)
            if not running:
                break
            if observer.poll() is not None:
                raise RuntimeError("capacity observer failed")
            time.sleep(0.1)
        report["exit_codes"] = [worker.returncode for worker in workers]
        # Stop descendants before reading logs or releasing the observer slot.
        for worker in workers:
            stop_once(worker)
        workers.clear()
        observer.stdin.close()
        report["peak_connections"] = int(observer.stdout.read())
        if observer.wait() != 0:
            raise RuntimeError("capacity observer failed")
        report["too_many_clients_lines"] = []
        for output in outputs:
            with output.open(errors="replace") as log:
                report["too_many_clients_lines"].append(sum(bool(re.search(r"53300|too many clients", line, re.I)) for line in log))
        return report
    finally:
        for worker in workers:
            stop_once(worker)
        if observer.stdin and not observer.stdin.closed:
            observer.stdin.close()
        if observer.poll() is None:
            observer.terminate()
        observer.wait()
        observer.stdout.close()
        if unconfirmed:
            raise RuntimeError("could not confirm that every check process stopped")


def exit_on_signal(signum, _frame):
    # SystemExit unwinds through measure's finally, which kills both check
    # process groups; the default SIGTERM/SIGHUP action would leave them running.
    sys.exit(128 + signum)


def main():
    for signum in (signal.SIGTERM, signal.SIGHUP):
        signal.signal(signum, exit_on_signal)
    roots = [Path(arg).resolve() for arg in sys.argv[1:3]]
    packages, parallel = map(int, sys.argv[3:5])
    if len(roots) != 2 or roots[0] == roots[1] or min(packages, parallel) <= 0:
        raise ValueError("invalid capacity arguments")
    if not all("$(GO_TEST_FLAGS)" in (root / "Makefile").read_text() for root in roots):
        raise ValueError("worktrees need configurable test flags")
    report = measure(roots, packages, parallel)
    print(json.dumps(report, indent=2))
    return int(any(report["exit_codes"]))


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, KeyError, RuntimeError):
        print("capacity run failed; check arguments, admin configuration and PostgreSQL availability", file=sys.stderr)
        sys.exit(1)
