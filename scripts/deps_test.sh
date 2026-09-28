#!/usr/bin/env bash
# Tests scripts/deps.sh on a copy of the module: a previously absent
# package-import edge makes --check fail until the file is regenerated, and
# regenerating adds exactly that edge line; an import through an edge that
# already exists changes nothing; regenerating twice is stable.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
fail() {
  echo "FAIL $*" >&2
  exit 1
}

copy=$work/module
mkdir -p "$copy/scripts" "$copy/docs"
cp "$root/go.mod" "$root/go.sum" "$copy/"
cp -R "$root/cmd" "$root/internal" "$root/db" "$root/web" "$copy/"
cp "$root/scripts/deps.sh" "$copy/scripts/"
cp "$root/docs/dependencies.md" "$copy/docs/"
module=$(cd "$copy" && go list -m)
graph=$copy/docs/dependencies.md
deps() { (cd "$copy" && bash scripts/deps.sh "$@"); }

deps --check >/dev/null 2>&1 || fail "the committed graph is stale; run make deps"
cp "$graph" "$work/before.md"
deps
cmp -s "$graph" "$work/before.md" || fail "regenerating twice is not stable"

# A new edge: a package that does not import internal/domain yet does.
source=internal/web/i18n
edge="$source -> internal/domain"
grep -qxF "$edge" "$graph" && fail "test setup: $edge already exists"
name=$(cd "$copy" && go list -f '{{.Name}}' "./$source")
printf 'package %s\n\nimport _ "%s/internal/domain"\n' "$name" "$module" >"$copy/$source/zz_new_edge.go"
if deps --check >/dev/null 2>&1; then
  fail "--check passed with a new edge not in the graph"
fi
deps
added=$(diff "$work/before.md" "$graph" | grep '^>' || true)
removed=$(diff "$work/before.md" "$graph" | grep '^<' || true)
[[ $added == "> $edge" && -z $removed ]] || fail "regenerating did not add exactly '$edge': added '$added', removed '$removed'"
deps --check >/dev/null 2>&1 || fail "--check failed right after regenerating"
echo "PASS new edge fails until regenerated, then adds one line"

# An existing edge, imported again from another file: nothing changes.
cp "$graph" "$work/with-edge.md"
existing=$(grep -m1 ' -> ' "$graph")
from=${existing%% -> *}
to=${existing##* -> }
name=$(cd "$copy" && go list -f '{{.Name}}' "./$from")
printf 'package %s\n\nimport _ "%s/%s"\n' "$name" "$module" "$to" >"$copy/$from/zz_same_edge.go"
deps --check >/dev/null 2>&1 || fail "--check failed for an import through an existing edge ($existing)"
deps
cmp -s "$graph" "$work/with-edge.md" || fail "an import through an existing edge changed the graph"
echo "PASS an import through an existing edge changes nothing"
