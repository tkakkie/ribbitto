"""Registry and database safety checks, including real subprocess contention."""
import fcntl
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import json
import multiprocessing as mp
import os
import queue
import shutil
from pathlib import Path
import socket
import subprocess
import tempfile
import threading
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
        with patch.object(env, "worktree_root", side_effect=lambda path: path.resolve()), patch.object(env, "worktrees", return_value=live), patch.object(env, "initial_port", return_value=candidate):
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
            live = {root.name: root for root in roots}
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
                self.assertEqual(len({port for entry in entries.values() for port in entry["ports"]}), 4)
                with patch.object(env, "worktree_root", side_effect=lambda path: path.resolve()), patch.object(env, "worktrees", return_value=live):
                    with patch.dict(os.environ, AI_APP_PORT=str(entries[roots[0].name]["ports"][0])):
                        with self.assertRaisesRegex(RuntimeError, "collision"):
                            env.configure(roots[1], common)
                    with socket.socket() as listener:
                        listener.bind(("127.0.0.1", 0))
                        with patch.dict(os.environ, AI_APP_PORT=str(listener.getsockname()[1])):
                            with self.assertRaisesRegex(RuntimeError, "collision"):
                                env.configure(roots[1], common)
                    deleted = common / "deleted"
                    entries["deleted"] = {"path": str(deleted), "database_path": str(deleted), "ports": entries[roots[0].name]["ports"]}
                    registry.write_text(json.dumps(entries))
                    env.configure(roots[0], common)
                    self.assertNotIn("deleted", json.loads(registry.read_text()))
                    first = (roots[0] / ".env.local").read_text().splitlines()[0].split("=")[1]
                    second = (roots[1] / ".env.local").read_text().splitlines()[0].split("=")[1]
                    env.configure(roots[0], common, clean=True)
                    for name in [second, template, "template0", "template1"]:
                        self.assertEqual(env.sql("SELECT count(*) FROM pg_database WHERE datname='" + name + "'"), "1")
                    self.assertEqual(env.sql("SELECT count(*) FROM pg_database WHERE datname='" + first + "'"), "0")
                    self.assertIn(roots[1].name, json.loads(registry.read_text()))
                    self.assertNotIn(roots[0].name, json.loads(registry.read_text()))
                    self.assertFalse((roots[0] / ".env.local").exists())
            finally:
                env.sql('ALTER DATABASE "' + template + '" IS_TEMPLATE false')
                env.sql('DROP DATABASE "' + template + '"')
                for worker in workers:
                    if worker.is_alive():
                        worker.terminate()
                        worker.join()
                with patch.object(env, "worktree_root", side_effect=lambda path: path.resolve()), patch.object(env, "worktrees", return_value=live):
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
            live = {"live": root}
            if listed:
                live["stale"] = stale
            registry = common / "ai-env.json"
            registry.write_text(json.dumps({"stale": {"path": str(stale), "database_path": str(stale), "ports": []}}))
            stale_name = env.database_name(str(stale))
            env.sql('CREATE DATABASE "' + stale_name + '" TEMPLATE template0')
            try:
                with patch.object(env, "worktree_root", side_effect=lambda path: path.resolve()), patch.object(env, "worktrees", return_value=live):
                    env.configure(root, common)
                    if listed:
                        self.assertIn("stale", json.loads(registry.read_text()))
                        self.assertEqual(env.sql("SELECT count(*) FROM pg_database WHERE datname='" + stale_name + "'"), "1")
                        del live["stale"]
                        env.configure(root, common)
                self.assertNotIn("stale", json.loads(registry.read_text()))
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
        for target, kwargs in (("worktree_root", {"side_effect": lambda path: path.resolve()}),
                               ("worktrees", {"return_value": {"live": self.root}})):
            mock = patch.object(env, target, **kwargs)
            mock.start()
            self.addCleanup(mock.stop)
        mock = patch.object(env.subprocess, "run", return_value=subprocess.CompletedProcess([], 0))
        mock.start()
        self.addCleanup(mock.stop)
        mock = patch.object(env, "sql", return_value="1")
        mock.start()
        self.addCleanup(mock.stop)

    def ports(self):
        return json.loads((self.common / "ai-env.json").read_text())["live"]["ports"]

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
        with patch.object(env, "free", return_value=False), patch.object(env, "owns_port", return_value=True):
            env.configure(self.root, self.common)
        self.assertEqual(self.ports(), previous)

    def test_rerun_replaces_foreign_listener(self):
        env.configure(self.root, self.common)
        previous = self.ports()
        requests = []
        class Foreign(BaseHTTPRequestHandler):
            def do_GET(self):
                requests.append(self.path)
                self.send_response(200)
                self.send_header("X-Ribbitto-Worktree", "other-worktree")
                self.end_headers()
            def log_message(self, *_):
                pass
        with ThreadingHTTPServer(("127.0.0.1", previous[0]), Foreign) as server:
            thread = threading.Thread(target=server.serve_forever)
            thread.start()
            try:
                env.configure(self.root, self.common)
                self.assertNotIn(previous[0], self.ports())
                self.assertIn("/healthz", requests)
            finally:
                server.shutdown()
                thread.join()

    def test_rerun_keeps_identified_listener(self):
        env.configure(self.root, self.common)
        previous = self.ports()
        marker = env.database_name(str(self.root))
        class Owned(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200)
                self.send_header("X-Ribbitto-Worktree", marker)
                self.end_headers()
            def log_message(self, *_):
                pass
        with ThreadingHTTPServer(("127.0.0.1", previous[0]), Owned) as server:
            thread = threading.Thread(target=server.serve_forever)
            thread.start()
            try:
                env.configure(self.root, self.common)
                self.assertEqual(previous, self.ports())
            finally:
                server.shutdown()
                thread.join()

    def test_override_range(self):
        for value in ("0", "70000"):
            with self.subTest(value=value), patch.dict(os.environ, AI_APP_PORT=value):
                with self.assertRaisesRegex(RuntimeError, "invalid override: AI_APP_PORT"):
                    env.configure(self.root, self.common)


class WorktreeTest(unittest.TestCase):
    def setUp(self):
        scratch = tempfile.TemporaryDirectory(dir="bin")
        self.addCleanup(scratch.cleanup)
        self.directory = Path(scratch.name).resolve()
        self.main = self.directory / "main"
        self.git("init", "-q", str(self.main), cwd=self.directory)
        self.git("-c", "user.name=Test", "-c", "user.email=test@example.org", "commit", "-q", "--allow-empty", "-m", "fixture")
        self.root = self.directory / "linked"
        self.git("worktree", "add", "-q", "--detach", str(self.root))
        self.common = env.common_dir(self.root)
        self.identity = env.current_id(self.root, env.worktrees(self.main, self.common))
        self.names = set()
        self.addCleanup(self.drop_databases)

    def git(self, *args, cwd=None):
        return subprocess.run(["git", *args], cwd=cwd or self.main, check=True, capture_output=True)

    def drop_databases(self):
        for name in self.names:
            env.sql('DROP DATABASE IF EXISTS "' + name + '" WITH (FORCE)')

    def configure(self, root):
        self.names.add(env.database_name(str(root.resolve())))
        env.configure(root, self.common)

    def entry(self):
        return json.loads((self.common / "ai-env.json").read_text())[self.identity]

    def mark_database(self):
        name = env.database_name(str(self.root))
        env.sql('COMMENT ON DATABASE "' + name + '" IS \'preserved\'')
        return name

    def assert_preserved(self, name, path):
        self.assertEqual(self.entry()["path"], str(path.resolve()))
        self.assertEqual(env.database_name(self.entry()["database_path"]), name)
        self.assertEqual(env.sql("SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname='" + name + "'"), "preserved")
        self.assertEqual(env.environment(path)["RIBBITTO_AI_WORKTREE"], name)

    def test_moved_worktree_keeps_database_and_entry(self):
        need_db(self)
        self.configure(self.root)
        name = self.mark_database()
        ports = self.entry()["ports"]
        moved = self.directory / "moved"
        self.git("worktree", "move", str(self.root), str(moved))
        self.configure(self.main)
        self.assert_preserved(name, moved)
        self.configure(moved)
        self.assert_preserved(name, moved)
        self.assertEqual(self.entry()["ports"], ports)

    def test_legacy_moved_worktree_keeps_database(self):
        need_db(self)
        self.configure(self.root)
        name = self.mark_database()
        ports = self.entry()["ports"]
        (self.common / "ai-env.json").write_text(json.dumps({str(self.root): ports}))
        moved = self.directory / "moved"
        self.git("worktree", "move", str(self.root), str(moved))
        self.configure(self.main)
        self.assert_preserved(name, moved)
        self.assertEqual(self.entry()["ports"], ports)

    def test_unknown_legacy_identity_refuses_cleanup(self):
        unknown = self.directory / "unknown"
        (self.common / "ai-env.json").write_text(json.dumps({str(unknown): [21000, 21001]}))
        with patch.object(env, "sql") as sql:
            with self.assertRaisesRegex(RuntimeError, "identity unknown; refusing stale cleanup"):
                env.configure(self.root, self.common)
            sql.assert_not_called()

    def test_symlink_spelling_keeps_database_and_entry(self):
        need_db(self)
        alias = self.directory / "alias"
        alias.symlink_to(self.directory, target_is_directory=True)
        # Exercise a spelling stored by Git that differs from Path.resolve().
        self.git("worktree", "move", str(self.root), str(alias / "spelled"))
        self.root = alias / "spelled"
        self.configure(self.root)
        name = env.database_name(str(self.root.resolve()))
        env.sql('COMMENT ON DATABASE "' + name + '" IS \'preserved\'')
        self.configure(self.root.resolve())
        self.configure(self.main)
        self.assert_preserved(name, self.root)

    def test_subdirectory_keeps_database_and_entry(self):
        need_db(self)
        child = self.root / "subdirectory"
        child.mkdir()
        self.configure(self.root)
        name = self.mark_database()
        self.configure(child)
        self.assert_preserved(name, child.parent)
        self.assertEqual(env.environment(child)["RIBBITTO_AI_WORKTREE"], name)
        env.configure(child, self.common, clean=True)
        self.assertNotIn(self.identity, json.loads((self.common / "ai-env.json").read_text()))
        self.assertEqual(env.sql("SELECT count(*) FROM pg_database WHERE datname='" + name + "'"), "0")

    def test_removed_worktree_is_cleaned(self):
        need_db(self)
        self.configure(self.root)
        name = self.mark_database()
        self.git("worktree", "remove", "--force", str(self.root))
        self.configure(self.main)
        self.assertNotIn(self.identity, json.loads((self.common / "ai-env.json").read_text()))
        self.assertEqual(env.sql("SELECT count(*) FROM pg_database WHERE datname='" + name + "'"), "0")

    def test_prunable_worktree_kept_until_id_is_gone(self):
        need_db(self)
        self.configure(self.root)
        name = self.mark_database()
        shutil.rmtree(self.root)
        self.configure(self.main)
        self.assertIn(self.identity, json.loads((self.common / "ai-env.json").read_text()))
        self.assertEqual(env.sql("SELECT count(*) FROM pg_database WHERE datname='" + name + "'"), "1")
        self.git("worktree", "prune")
        self.configure(self.main)
        self.assertNotIn(self.identity, json.loads((self.common / "ai-env.json").read_text()))
        self.assertEqual(env.sql("SELECT count(*) FROM pg_database WHERE datname='" + name + "'"), "0")

    def test_run_rejects_unowned_database_or_ports_before_connection(self):
        real_run = subprocess.run
        with patch.object(env, "sql", return_value="1"), patch.object(env.subprocess, "run", wraps=subprocess.run) as commands:
            # Suppress only the migration helper, keeping the Git identity checks real.
            commands.side_effect = lambda args, **kwargs: subprocess.CompletedProcess([], 0) if args[0] == str(env.HELPER) else real_run(args, **kwargs)
            env.configure(self.root, self.common)
        local = self.root / ".env.local"
        original = local.read_text()
        for field, value in (("AI_DATABASE", "ribbitto"), ("AI_DATABASE", "x?host=evil.example"),
                             ("AI_APP_PORT", "1"), ("AI_METRICS_PORT", "2"),
                             ("AI_APP_PORT", "not-an-integer"), ("AI_METRICS_PORT", "70000")):
            values = dict(line.split("=", 1) for line in original.splitlines())
            values[field] = value
            local.write_text("".join(key + "=" + value + "\n" for key, value in values.items()))
            with self.subTest(field=field, value=value), patch.object(env, "sql") as sql, patch.dict(os.environ, RIBBITTO_TEST_DATABASE_URL="invalid-admin-config"):
                with self.assertRaisesRegex(RuntimeError, ".env.local " + field + " does not match"):
                    env.environment(self.root)
                sql.assert_not_called()
                result = subprocess.run(["python3", str(Path(env.__file__).resolve()), "run", "invalid-command-must-not-execute"], cwd=self.root, capture_output=True, text=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(".env.local " + field + " does not match", result.stderr)
                self.assertNotIn("Traceback", result.stderr)
        local.write_text(original)

    def test_run_rebuilds_database_field_preserving_admin_target(self):
        need_db(self)
        self.configure(self.root)
        admin = env.urlsplit(os.environ["RIBBITTO_TEST_DATABASE_URL"])
        query = env.parse_qsl(admin.query, keep_blank_values=True)
        query.extend((("dbname", "postgres"), ("application_name", "value?host=other&setting=text")))
        with patch.dict(os.environ, RIBBITTO_TEST_DATABASE_URL=env.urlunsplit(admin._replace(query=env.urlencode(query)))):
            result = env.urlsplit(env.environment(self.root)["RIBBITTO_DATABASE_URL"])
        # Assert fields separately so a failure never exposes a credentialed URL.
        self.assertTrue(result.netloc == admin.netloc)
        self.assertEqual(result.path, "/" + env.database_name(str(self.root)))
        self.assertTrue(env.parse_qsl(result.query, keep_blank_values=True) == [(key, value) for key, value in query if key != "dbname"])
