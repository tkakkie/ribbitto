"""Worktree dev configuration. Credentials never enter the registry or files."""
import fcntl
import hashlib
import http.client
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
from urllib.parse import parse_qsl, quote, urlencode, urlsplit, urlunsplit

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
    root = worktree_root(root)
    if not (root / ".env.local").exists():
        return os.environ.copy()
    values = dict(line.split("=", 1) for line in (root / ".env.local").read_text().splitlines())
    common = common_dir(root)
    with (common / "ai-env.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_SH)
        entries = json.loads((common / "ai-env.json").read_text())
        key = current_id(root, worktrees(root, common))
        entry = entries.get(key)
        if not entry or values.get("AI_DATABASE") != database_name(entry["database_path"]):
            raise RuntimeError(".env.local AI_DATABASE does not match this worktree; run make ai-env")
        if len(entry["ports"]) != 2:
            raise RuntimeError("worktree port reservations invalid; run make ai-env")
        for field, port in zip(("AI_APP_PORT", "AI_METRICS_PORT"), entry["ports"]):
            if type(port) is not int or not 1 <= port <= 65535 or values.get(field) != str(port):
                raise RuntimeError(".env.local " + field + " does not match this worktree's reserved port; run make ai-env")
    url = urlsplit(os.environ["RIBBITTO_TEST_DATABASE_URL"])
    if url.scheme not in ("postgres", "postgresql") or not url.hostname:
        raise RuntimeError("admin configuration must be a PostgreSQL URL")
    # Only the database field changes; query database aliases cannot override it.
    query = [(key, value) for key, value in parse_qsl(url.query, keep_blank_values=True)
             if key not in ("dbname", "database")]
    env = os.environ.copy()
    env.update(RIBBITTO_DATABASE_URL=urlunsplit(url._replace(path="/" + quote(values["AI_DATABASE"], safe=""),
                                                           query=urlencode(query))),
               RIBBITTO_ADDR="127.0.0.1:" + values["AI_APP_PORT"],
               RIBBITTO_DEV_METRICS_ADDR="127.0.0.1:" + values["AI_METRICS_PORT"],
               RIBBITTO_AI_WORKTREE=database_name(entry["database_path"]))
    return env

def worktree_root(path):
    return Path(subprocess.check_output(["git", "rev-parse", "--show-toplevel"], cwd=path, text=True).strip()).resolve()

def common_dir(root):
    path = Path(subprocess.check_output(["git", "rev-parse", "--git-common-dir"], cwd=root, text=True).strip())
    return (root / path).resolve()

def worktrees(root, common):
    live = subprocess.check_output(["git", "worktree", "list", "--porcelain", "-z"], cwd=root)
    paths = [Path(os.fsdecode(item[9:])).resolve() for item in live.split(b"\0") if item.startswith(b"worktree ")]
    ids = {paths[0]: "main"}
    metadata = common / "worktrees"
    if metadata.exists():
        for directory in metadata.iterdir():
            # Git keeps this backlink, including for a moved or prunable worktree.
            path = Path((directory / "gitdir").read_text().strip()).parent.resolve()
            ids[path] = "worktree:" + directory.name
    if any(path not in ids for path in paths):
        raise RuntimeError("cannot identify Git worktree; refusing stale cleanup")
    return {ids[path]: path for path in paths}

def current_id(root, live):
    for key, path in live.items():
        if path.resolve() == root.resolve():
            return key
    raise RuntimeError("current Git worktree not listed; refusing cleanup")

def owns_port(port, marker):
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=0.5)
    try:
        connection.request("GET", "/healthz")
        response = connection.getresponse()
        return response.status == 200 and response.getheader("X-Ribbitto-Worktree") == marker
    except (OSError, http.client.HTTPException):
        return False
    finally:
        connection.close()

def initial_port(key):
    return 20000 + int(hashlib.sha256(key.encode()).hexdigest()[:8], 16) % 12768

def database_name(key):
    return "ribbitto_dev_" + hashlib.sha256(key.encode()).hexdigest()[:20]

def local_configuration(name, ports):
    return f"AI_DATABASE={name}\nAI_APP_PORT={ports[0]}\nAI_METRICS_PORT={ports[1]}\n"

def configure(root, common, clean=False):
    root = worktree_root(root)
    registry = common / "ai-env.json"
    with (common / "ai-env.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        entries = json.loads(registry.read_text()) if registry.exists() else {}
        live = worktrees(root, common)
        key = current_id(root, live)
        # Upgrade path-keyed reservations only when their live identity is known.
        for old, ports in list(entries.items()):
            if isinstance(ports, list):
                matches = [identity for identity, path in live.items() if path.resolve() == Path(old).resolve()]
                if not matches and len(ports) == 2:
                    expected = local_configuration(database_name(str(Path(old).resolve())), ports)
                    matches = [identity for identity, path in live.items()
                               if (path / ".env.local").exists() and (path / ".env.local").read_text() == expected]
                if len(matches) != 1 or matches[0] in entries:
                    raise RuntimeError("legacy worktree identity unknown; refusing stale cleanup")
                entries[matches[0]] = {"path": str(live[matches[0]]), "database_path": str(Path(old).resolve()), "ports": ports}
                del entries[old]
        for identity, entry in list(entries.items()):
            if identity not in live:
                sql('DROP DATABASE IF EXISTS "' + database_name(entry["database_path"]) + '" WITH (FORCE)')
                del entries[identity]
            else:
                entry["path"] = str(live[identity].resolve())
        entry = entries.get(key, {"path": str(root), "database_path": str(root), "ports": []})
        name = database_name(entry["database_path"])
        if clean:
            sql('DROP DATABASE IF EXISTS "' + name + '" WITH (FORCE)')
            entries.pop(key, None)
            (root / ".env.local").unlink(missing_ok=True)
        else:
            used = {port for identity, entry in entries.items() if identity != key for port in entry["ports"]}
            previous = entry["ports"]
            ports = []
            for i, override in enumerate(("AI_APP_PORT", "AI_METRICS_PORT")):
                explicit = os.environ.get(override)
                candidate = int(explicit) if explicit else (previous[i] if previous else initial_port(str(root)))
                for offset in range(12768 if not explicit else 1):
                    port = candidate if explicit or offset == 0 else 20000 + (candidate - 20000 + offset) % 12768
                    if 1 <= port <= 65535 and port not in used and (free(port) or
                            (not explicit and port in previous and owns_port(port, name))):
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
            entry["ports"] = ports
            entries[key] = entry
            atomic(root / ".env.local", local_configuration(name, ports))
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
        common = common_dir(root)
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
