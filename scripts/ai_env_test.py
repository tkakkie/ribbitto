"""Registry and database safety checks, including real subprocess contention."""
import fcntl
import importlib.util
import json
import multiprocessing as mp
import os
import queue
from pathlib import Path
import socket
import subprocess
import tempfile
import unittest
from unittest.mock import patch
spec = importlib.util.spec_from_file_location("ai_env", os.environ.get("AI_ENV_SOURCE", "scripts/ai_env.py"))
env = importlib.util.module_from_spec(spec)
spec.loader.exec_module(env)

def need_db(test):
    if "RIBBITTO_TEST_DATABASE_URL" not in os.environ and os.environ.get("RIBBITTO_REQUIRE_DB") != "1":
        test.skipTest("admin configuration unset")

def allocate(root, common, live, candidate, result):
    try:
        with patch.object(env.subprocess, "check_output", return_value=live), patch.object(env, "initial_port", return_value=candidate):
            env.configure(root, common)
        result.put("ok")
    except Exception:
        result.put("failed")

class EnvironmentTest(unittest.TestCase):
    def test_override_range_cli(self):
        need_db(self)
        with tempfile.TemporaryDirectory(dir="bin") as scratch:
            root = Path(scratch).resolve()
            subprocess.run(["git", "init", "-q", str(root)], check=True, capture_output=True)
            try:
                for value in ("0", "70000"):
                    bad = os.environ.copy()
                    bad["AI_APP_PORT"] = value
                    result = subprocess.run(["python3", str(Path(env.__file__).resolve()), "ai-env"],
                                            cwd=root, env=bad, capture_output=True, text=True)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn("invalid override: AI_APP_PORT", result.stderr)
                    self.assertNotIn("Traceback", result.stderr)
            finally:
                env.sql('DROP DATABASE IF EXISTS "' + env.database_name(str(root)) + '" WITH (FORCE)')

    def test_health_configuration_and_sql_errors(self):
        missing = os.environ.copy()
        missing.pop("RIBBITTO_TEST_DATABASE_URL", None)
        result = subprocess.run(["python3", str(Path(env.__file__)), "health"], env=missing, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("admin URL not set", result.stderr)
        self.assertNotIn("stopped or unreachable", result.stderr)
        need_db(self)
        with self.assertRaisesRegex(RuntimeError, "statement failed"):
            env.sql("SELECT deliberately_missing_ai_env_column")

    def test_health_redacts(self):
        bad = os.environ.copy()
        bad["RIBBITTO_TEST_DATABASE_URL"] = env.urlunsplit(("postgres", "private:" + "secret-canary" + "@127.0.0.1:1", "/postgres", "connect_timeout=1", ""))
        result = subprocess.run(["python3", str(Path(env.__file__)), "health"], env=bad, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("stopped or unreachable", result.stderr)
        self.assertNotIn("secret-canary", result.stdout + result.stderr)
        self.assertNotIn("postgres://", result.stdout + result.stderr)

    def test_registry_and_cleanup(self):
        if "RIBBITTO_TEST_DATABASE_URL" not in os.environ and os.environ.get("RIBBITTO_REQUIRE_DB") != "1":
            self.skipTest("admin configuration unset")
        with tempfile.TemporaryDirectory(dir="bin") as scratch:
            common = Path(scratch).resolve()
            roots = [common / name for name in ("first", "second")]
            for root in roots:
                root.mkdir()
                (root / ".git").touch()
            live = b"\0".join(b"worktree " + os.fsencode(root) for root in roots)
            ctx = mp.get_context("fork")
            results = ctx.Queue()
            template = "ribbitto_tmpl_ai_" + common.name.replace("-", "_")
            env.sql('CREATE DATABASE "' + template + '" TEMPLATE template0 IS_TEMPLATE true ALLOW_CONNECTIONS false')
            with socket.socket() as listener:
                listener.bind(("127.0.0.1", 0))
                candidate = listener.getsockname()[1]
            workers = [ctx.Process(target=allocate, args=(root, common, live, candidate, results)) for root in roots]
            try:
                with (common / "ai-env.lock").open("a") as lock:
                    fcntl.flock(lock, fcntl.LOCK_EX)
                    for worker in workers:
                        worker.start()
                    with self.assertRaises(queue.Empty):
                        results.get(timeout=1)
                    fcntl.flock(lock, fcntl.LOCK_UN)
                for worker in workers:
                    worker.join(90)
                    self.assertEqual(worker.exitcode, 0)
                    self.assertEqual(results.get(timeout=1), "ok")
                registry = common / "ai-env.json"
                entries = json.loads(registry.read_text())
                self.assertEqual(len({port for ports in entries.values() for port in ports}), 4)
                with patch.object(env.subprocess, "check_output", return_value=live):
                    with patch.dict(os.environ, AI_APP_PORT=str(entries[str(roots[0])][0])):
                        with self.assertRaisesRegex(RuntimeError, "collision"):
                            env.configure(roots[1], common)
                    with socket.socket() as listener:
                        listener.bind(("127.0.0.1", 0))
                        with patch.dict(os.environ, AI_APP_PORT=str(listener.getsockname()[1])):
                            with self.assertRaisesRegex(RuntimeError, "collision"):
                                env.configure(roots[1], common)
                    entries[str(common / "deleted")] = entries[str(roots[0])]
                    registry.write_text(json.dumps(entries))
                    env.configure(roots[0], common)
                    self.assertNotIn(str(common / "deleted"), json.loads(registry.read_text()))
                    first = (roots[0] / ".env.local").read_text().splitlines()[0].split("=")[1]
                    second = (roots[1] / ".env.local").read_text().splitlines()[0].split("=")[1]
                    env.configure(roots[0], common, clean=True)
                    for name in [second, template, "template0", "template1"]:
                        self.assertEqual(env.sql("SELECT count(*) FROM pg_database WHERE datname='" + name + "'"), "1")
                    self.assertEqual(env.sql("SELECT count(*) FROM pg_database WHERE datname='" + first + "'"), "0")
                    self.assertIn(str(roots[1]), json.loads(registry.read_text()))
                    self.assertNotIn(str(roots[0]), json.loads(registry.read_text()))
                    self.assertFalse((roots[0] / ".env.local").exists())
            finally:
                env.sql('ALTER DATABASE "' + template + '" IS_TEMPLATE false')
                env.sql('DROP DATABASE "' + template + '"')
                for worker in workers:
                    if worker.is_alive():
                        worker.terminate()
                        worker.join()
                with patch.object(env.subprocess, "check_output", return_value=live):
                    for root in roots:
                        env.configure(root, common, clean=True)

    def stale_case(self, listed, metadata):
        need_db(self)
        with tempfile.TemporaryDirectory(dir="bin") as scratch:
            common = Path(scratch).resolve()
            root, stale = common / "live", common / "stale"
            root.mkdir()
            (root / ".git").touch()
            if metadata:
                stale.mkdir()
                (stale / ".git").touch()
            live = b"worktree " + os.fsencode(root) + b"\0"
            if listed:
                live += b"worktree " + os.fsencode(stale) + b"\0prunable gitdir file points to non-existent location\0"
            registry = common / "ai-env.json"
            registry.write_text(json.dumps({str(stale): []}))
            stale_name = env.database_name(str(stale))
            env.sql('CREATE DATABASE "' + stale_name + '" TEMPLATE template0')
            try:
                with patch.object(env.subprocess, "check_output", return_value=live):
                    env.configure(root, common)
                self.assertNotIn(str(stale), json.loads(registry.read_text()))
                self.assertEqual(env.sql("SELECT count(*) FROM pg_database WHERE datname='" + stale_name + "'"), "0")
            finally:
                for path in (root, stale):
                    env.sql('DROP DATABASE IF EXISTS "' + env.database_name(str(path)) + '" WITH (FORCE)')

    def test_stale_missing_from_git(self):
        self.stale_case(listed=False, metadata=True)

    def test_stale_prunable_without_metadata(self):
        self.stale_case(listed=True, metadata=False)


class PortTest(unittest.TestCase):
    def setUp(self):
        self.scratch = tempfile.TemporaryDirectory(dir="bin")
        self.addCleanup(self.scratch.cleanup)
        self.common = Path(self.scratch.name).resolve()
        self.root = self.common / "live"
        self.root.mkdir()
        (self.root / ".git").touch()
        for target, kwargs in (("check_output", {"return_value": b"worktree " + os.fsencode(self.root) + b"\0"}),
                               ("run", {"return_value": subprocess.CompletedProcess([], 0)})):
            mock = patch.object(env.subprocess, target, **kwargs)
            mock.start()
            self.addCleanup(mock.stop)
        mock = patch.object(env, "sql", return_value="1")
        mock.start()
        self.addCleanup(mock.stop)

    def ports(self):
        return json.loads((self.common / "ai-env.json").read_text())[str(self.root)]

    def test_automatic_skips_listener(self):
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
            with patch.object(env, "initial_port", return_value=port):
                env.configure(self.root, self.common)
            self.assertNotIn(port, self.ports())

    def test_rerun_keeps_ports(self):
        env.configure(self.root, self.common)
        previous = self.ports()
        with patch.object(env, "initial_port", return_value=previous[-1] + 1):
            env.configure(self.root, self.common)
        self.assertEqual(self.ports(), previous)
        with patch.object(env, "free", return_value=False):
            env.configure(self.root, self.common)
        self.assertEqual(self.ports(), previous)

    def test_override_range(self):
        for value in ("0", "70000"):
            with self.subTest(value=value), patch.dict(os.environ, AI_APP_PORT=value):
                with self.assertRaisesRegex(RuntimeError, "invalid override: AI_APP_PORT"):
                    env.configure(self.root, self.common)
