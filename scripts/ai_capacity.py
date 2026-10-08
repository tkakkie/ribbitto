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
from urllib.parse import parse_qs, urlsplit
import ai_env


def stop_group(worker):
    # The make parent may already have exited while a child is still alive.
    try:
        os.killpg(worker.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    worker.wait()


def measure(roots, packages, parallel):
    settings = parse_qs(urlsplit(os.environ["RIBBITTO_TEST_DATABASE_URL"]).query)
    cpus = len(os.sched_getaffinity(0)) if hasattr(os, "sched_getaffinity") else os.cpu_count()
    report = dict(packages=packages, parallel=parallel, count=1, require_db=1,
                  pgx_pool_max_conns=int(settings.get("pool_max_conns", [max(4, cpus)])[0]),
                  migration_pool_max_open=0, admin_connections_per_pgtest_call=1)
    workers, outputs = [], []
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
        while any(worker.poll() is None for worker in workers):
            if observer.poll() is not None:
                raise RuntimeError("capacity observer failed")
            time.sleep(0.1)
        report["exit_codes"] = [worker.returncode for worker in workers]
        # Stop descendants before reading logs or releasing the observer slot.
        for worker in workers:
            stop_group(worker)
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
            stop_group(worker)
        if observer.stdin and not observer.stdin.closed:
            observer.stdin.close()
        if observer.poll() is None:
            observer.terminate()
        observer.wait()
        observer.stdout.close()


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
