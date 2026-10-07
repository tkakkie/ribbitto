"""Worktree dev configuration. Credentials never enter the registry or files."""
import fcntl
import hashlib
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
from urllib.parse import urlsplit, urlunsplit

HELPER = Path(__file__).resolve().parents[1] / "bin/ai-db"

def sql(query):
    result = subprocess.run([str(HELPER)], input=query, text=True, capture_output=True)
    if result.returncode:
        raise RuntimeError({2: "admin URL not set or invalid",
                            3: "PostgreSQL stopped or unreachable",
                            4: "PostgreSQL statement failed; check admin permissions"}.get(
                                result.returncode, "PostgreSQL helper failed"))
    return result.stdout.strip()

def atomic(path, value):
    fd, name = tempfile.mkstemp(dir=path.parent)
    try:
        with os.fdopen(fd, "w") as out:
            out.write(value)
        os.replace(name, path)
    finally:
        Path(name).unlink(missing_ok=True)

def free(port):
    for host in ("0.0.0.0", "::"):
        family = socket.AF_INET if host == "0.0.0.0" else socket.AF_INET6
        with socket.socket(family) as listener:
            try:
                listener.bind((host, port))
            except OSError:
                return False
    return True

def environment(root):
    if not (root / ".env.local").exists():
        return os.environ.copy()
    values = dict(line.split("=", 1) for line in (root / ".env.local").read_text().splitlines())
    url = urlsplit(os.environ["RIBBITTO_TEST_DATABASE_URL"])
    if url.scheme not in ("postgres", "postgresql") or not url.hostname:
        raise RuntimeError("admin configuration must be a PostgreSQL URL")
    env = os.environ.copy()
    env.update(RIBBITTO_DATABASE_URL=urlunsplit(url._replace(path="/" + values["AI_DATABASE"])),
               RIBBITTO_ADDR="127.0.0.1:" + values["AI_APP_PORT"],
               RIBBITTO_DEV_METRICS_ADDR="127.0.0.1:" + values["AI_METRICS_PORT"])
    return env

def initial_port(key):
    return 20000 + int(hashlib.sha256(key.encode()).hexdigest()[:8], 16) % 12768

def database_name(key):
    return "ribbitto_dev_" + hashlib.sha256(key.encode()).hexdigest()[:20]

def configure(root, common, clean=False):
    key = str(root.resolve())
    name = database_name(key)
    registry = common / "ai-env.json"
    with (common / "ai-env.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        entries = json.loads(registry.read_text()) if registry.exists() else {}
        # A stale directory alone does not prove that its worktree still exists.
        live = subprocess.check_output(["git", "worktree", "list", "--porcelain", "-z"], cwd=root)
        paths = {os.fsdecode(item[9:]) for item in live.split(b"\0") if item.startswith(b"worktree ")}
        for path in list(entries):
            if not (path in paths and (Path(path) / ".git").exists()):
                # Only registered paths can own databases removed by stale cleanup.
                sql('DROP DATABASE IF EXISTS "' + database_name(path) + '" WITH (FORCE)')
                del entries[path]
        if clean:
            sql('DROP DATABASE IF EXISTS "' + name + '" WITH (FORCE)')
            entries.pop(key, None)
            (root / ".env.local").unlink(missing_ok=True)
        else:
            used = {port for path, ports in entries.items() if path != key for port in ports}
            previous = entries.get(key, [])
            ports = []
            for i, override in enumerate(("AI_APP_PORT", "AI_METRICS_PORT")):
                explicit = os.environ.get(override)
                candidate = int(explicit) if explicit else (previous[i] if previous else initial_port(key))
                for offset in range(12768 if not explicit else 1):
                    port = candidate if explicit or offset == 0 else 20000 + (candidate - 20000 + offset) % 12768
                    if 1 <= port <= 65535 and port not in used and ((not explicit and port in previous) or free(port)):
                        break
                else:
                    raise RuntimeError("port collision or invalid override: " + override)
                ports.append(port)
                used.add(port)
            if sql("SELECT count(*) FROM pg_database WHERE datname='" + name + "'") == "0":
                sql('CREATE DATABASE "' + name + '" TEMPLATE template0')
            result = subprocess.run([str(HELPER), name], capture_output=True)
            if result.returncode:
                raise RuntimeError("dev database migration failed")
            entries[key] = ports
            atomic(root / ".env.local", f"AI_DATABASE={name}\nAI_APP_PORT={ports[0]}\nAI_METRICS_PORT={ports[1]}\n")
        atomic(registry, json.dumps(entries) + "\n")

def main():
    root = Path.cwd()
    action = sys.argv[1]
    if action == "run":
        os.execvpe(sys.argv[2], sys.argv[2:], environment(root))
    elif action == "health":
        sql("SELECT 1")
        print("PostgreSQL reachable")
    else:
        common = Path(subprocess.check_output(["git", "rev-parse", "--git-common-dir"], text=True).strip()).resolve()
        configure(root, common, action == "clean")

if __name__ == "__main__":
    try:
        main()
    except RuntimeError as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
    except (OSError, ValueError, KeyError, subprocess.SubprocessError):
        print("ai-env failed: check ports, PostgreSQL availability and admin configuration", file=sys.stderr)
        sys.exit(1)
