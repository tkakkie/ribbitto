"""Exercise capacity reporting and process cleanup without running make check."""
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
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
            with patch.dict(os.environ, PATH=str(base) + os.pathsep + os.environ["PATH"],
                            GOMODCACHE="inherited-module-cache", GOPATH="inherited-gopath"):
                report = ai_capacity.measure(roots, 2, 3)
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
            # Closing stdin ends observation and releases the reserved connection.
            observer.stdin.close()
            peak = int(observer.stdout.read())
            self.assertEqual(observer.wait(timeout=10), 0)
            self.assertGreaterEqual(peak, baseline["baseline"])
        finally:
            if not observer.stdin.closed:
                observer.stdin.close()
            if observer.poll() is None:
                observer.kill()
            observer.wait()
            observer.stdout.close()
