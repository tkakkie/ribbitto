"""Exercise capacity reporting and process cleanup without running make check."""
import json
import os
from pathlib import Path
import signal
import socket
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch
import ai_capacity
import ai_env


class CapacityTest(unittest.TestCase):
    def setUp(self):
        if "RIBBITTO_TEST_DATABASE_URL" not in os.environ and os.environ.get("RIBBITTO_REQUIRE_DB") != "1":
            self.skipTest("admin configuration unset")

    def test_report_private_logs_and_descendant_cleanup(self):
        with tempfile.TemporaryDirectory(dir="bin") as scratch:
            base = Path(scratch).resolve()
            roots = [base / name for name in ("first", "second")]
            for root in roots:
                root.mkdir()
                (root / "Makefile").write_text("$(GO_TEST_FLAGS)\n")
            fake_make = base / "make"
            fake_make.write_text("#!" + sys.executable + "\n" + '''
import os, subprocess, sys
assert os.environ["GOMODCACHE"] == "inherited-module-cache"
assert os.environ["GOPATH"] == "inherited-gopath"
assert os.environ["RIBBITTO_REQUIRE_DB"] == "1"
assert sys.argv[1:] == ["check", "GO_TEST_FLAGS=-count=1 -p 2 -parallel 3"]
child = subprocess.Popen([sys.executable, "-c", "import socket, time; s = socket.socket(); s.bind(('127.0.0.1', 0)); s.listen(); print(s.getsockname()[1], flush=True); time.sleep(60)"], stdout=subprocess.PIPE, text=True)
print(child.stdout.readline().strip(), flush=True)
print("private postgres://credential-canary@example/db", flush=True)
print("SQLSTATE 53300: too many clients", flush=True)
print("too many clients", flush=True)
sys.exit(7 if os.getcwd().endswith("second") else 0)
''')
            fake_make.chmod(0o700)
            events, popen = [], subprocess.Popen

            def recording_popen(args, *rest, **options):
                process = popen(args, *rest, **options)
                if args[-1] != "--observe":
                    if args[0] == "make":
                        events.append("check")
                    return process
                stdout = process.stdout

                class Handshake:
                    def readline(self):
                        line = stdout.readline()
                        events.append("handshake")
                        return line

                    def __getattr__(self, name):
                        return getattr(stdout, name)

                process.stdout = Handshake()
                return process

            with patch.dict(os.environ, PATH=str(base) + os.pathsep + os.environ["PATH"],
                            GOMODCACHE="inherited-module-cache", GOPATH="inherited-gopath"), \
                    patch.object(ai_capacity.subprocess, "Popen", recording_popen):
                report = ai_capacity.measure(roots, 2, 3)
            # The observer must own its connection before either check starts.
            self.assertEqual(events, ["handshake", "check", "check"])
            self.assertEqual(report["exit_codes"], [0, 7])
            self.assertEqual(report["too_many_clients_lines"], [2, 2])
            self.assertGreaterEqual(report["peak_connections"], report["baseline"])
            self.assertNotIn("postgres://", json.dumps(report))
            self.assertNotIn("credential-canary", json.dumps(report))
            for root in roots:
                logs = list((root / "bin").glob("ai-capacity-*.log"))
                self.assertEqual(len(logs), 1)
                self.assertEqual(logs[0].stat().st_mode & 0o777, 0o600)
                port = int(logs[0].read_text().splitlines()[0])
                with socket.socket() as client:
                    self.assertNotEqual(client.connect_ex(("127.0.0.1", port)), 0)

    def test_observer_connection_precedes_checks(self):
        observer = subprocess.Popen([str(ai_env.HELPER), "--observe"], stdin=subprocess.PIPE,
                                    stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
        try:
            baseline = json.loads(observer.stdout.readline())
            self.assertGreater(baseline["baseline"], 0)
            # A second observer holds one more connection; sampling must see it.
            extra = subprocess.Popen([str(ai_env.HELPER), "--observe"], stdin=subprocess.PIPE,
                                     stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
            try:
                json.loads(extra.stdout.readline())
                time.sleep(0.5)  # several 100 ms samples while the extra connection is open
            finally:
                extra.stdin.close()
                extra.stdout.read()
                extra.wait(timeout=10)
                extra.stdout.close()
            # Closing stdin ends observation and releases the reserved connection.
            observer.stdin.close()
            peak = int(observer.stdout.read())
            self.assertEqual(observer.wait(timeout=10), 0)
            self.assertGreaterEqual(peak, baseline["baseline"] + 1)
        finally:
            if not observer.stdin.closed:
                observer.stdin.close()
            if observer.poll() is None:
                observer.kill()
            observer.wait()
            observer.stdout.close()

    def test_sigterm_kills_both_check_groups_and_detached_descendants(self):
        with tempfile.TemporaryDirectory(dir="bin") as scratch:
            base = Path(scratch).resolve()
            roots = [base / name for name in ("first", "second")]
            for root in roots:
                root.mkdir()
                (root / "Makefile").write_text("$(GO_TEST_FLAGS)\n")
            fake_make = base / "make"
            # Each fake check starts a grandchild, records both PIDs, then waits.
            fake_make.write_text("#!" + sys.executable + "\n" + '''
import os, subprocess, sys, time
child = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(120)"])
# Like Chromium under the browser test, this one leaves the check's group,
# then starts a process of its own there.
detached = subprocess.Popen([sys.executable, "-c", (
    "import os, subprocess, sys, time; os.setpgrp(); "
    "spawned = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(120)']); "
    "open('spawned.tmp', 'w').write(str(spawned.pid)); os.rename('spawned.tmp', 'spawned'); "
    "time.sleep(120)")])
while not os.path.exists("spawned"):
    time.sleep(0.01)
with open("spawned") as source:
    spawned = int(source.read())
with open("pids.tmp", "w") as out:
    out.write(f"{os.getpid()} {child.pid} {detached.pid} {spawned}")
os.rename("pids.tmp", "pids")
time.sleep(120)
''')
            fake_make.chmod(0o700)
            environment = dict(os.environ, PATH=str(base) + os.pathsep + os.environ["PATH"])
            runner = subprocess.Popen([sys.executable, str(Path(ai_capacity.__file__)), *map(str, roots), "1", "1"],
                                      env=environment, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            pids = []
            try:
                pid_files = [root / "pids" for root in roots]
                deadline = time.monotonic() + 30
                while not all(path.exists() for path in pid_files):
                    self.assertIsNone(runner.poll(), "runner exited before both checks started")
                    self.assertLess(time.monotonic(), deadline, "checks did not start")
                    time.sleep(0.05)
                pids = [int(pid) for path in pid_files for pid in path.read_text().split()]
                runner.send_signal(signal.SIGTERM)
                self.assertEqual(runner.wait(timeout=30), 128 + signal.SIGTERM)
                for pid in pids:
                    deadline = time.monotonic() + 10
                    while alive(pid):
                        self.assertLess(time.monotonic(), deadline, f"process {pid} survived SIGTERM")
                        time.sleep(0.05)
            finally:
                if runner.poll() is None:
                    runner.kill()
                    runner.wait()
                # On failure, do not leave the fake checks running.
                for pid in pids:
                    for send in (os.killpg, os.kill):
                        try:
                            send(pid, signal.SIGKILL)
                        except (ProcessLookupError, PermissionError):
                            pass


def alive(pid):
    try:
        os.kill(pid, 0)
    except ProcessLookupError:
        return False
    # A zombie still answers kill(0); ps reports it as Z once its group is gone.
    state = subprocess.run(["ps", "-o", "stat=", "-p", str(pid)], capture_output=True, text=True).stdout.strip()
    return bool(state) and not state.startswith("Z")


class CleanupTest(unittest.TestCase):
    """Cleanup needs no database, so these tests always run."""

    def test_cleanup_falls_back_to_the_group_when_ps_fails(self):
        worker = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(120)"], start_new_session=True)
        try:
            with patch.object(ai_capacity.subprocess, "run", side_effect=subprocess.TimeoutExpired("ps", 10)):
                ai_capacity.stop_group(worker)
            self.assertIsNotNone(worker.returncode)
        finally:
            if worker.poll() is None:
                worker.kill()
                worker.wait()

    def test_cleanup_rescans_for_members_forked_after_a_scan(self):
        # Each scan finds a process the previous kill could not have known.
        scans = iter([[101], [102], []])
        killed = []
        worker = subprocess.Popen([sys.executable, "-c", "pass"], start_new_session=True)
        worker.wait()
        with patch.object(ai_capacity, "session_members", side_effect=lambda session: next(scans)), \
                patch.object(ai_capacity, "kill_member", side_effect=lambda pid, session: killed.append(pid)):
            ai_capacity.stop_group(worker)
        self.assertEqual(killed, [101, 102])
