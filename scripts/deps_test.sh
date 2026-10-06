#!/usr/bin/env bash
# Tests scripts/deps.sh on a copy of the module: a previously absent
# package-import edge makes --check fail until the file is regenerated, and
# regenerating adds exactly that edge line; an import through an edge that
# already exists changes nothing; regenerating twice is stable. --diff
# compares only edges, groups them by unit, and validates its revision.
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
# Determinism: two successive generations are byte-identical.
deps
cp "$graph" "$work/first.md"
deps
cmp -s "$graph" "$work/first.md" || fail "two successive generations differ"
cp "$graph" "$work/before.md"

# History belongs only to the disposable module copy, never the worktree.
git -C "$copy" init -q
git -C "$copy" config user.name 'Dependency test'
git -C "$copy" config user.email 'deps-test@example.invalid'
git -C "$copy" -c commit.gpgsign=false commit -q --allow-empty -m 'Without graph'
git -C "$copy" add docs/dependencies.md
git -C "$copy" -c commit.gpgsign=false commit -q -m 'Baseline graph'
no_change='No package-import edges changed.'
[[ $(deps --diff HEAD) == "$no_change" ]] || fail "unchanged graph diff"
awk 'NR == 1 { $0 = $0 " (header changed)" } { print }' "$work/before.md" > "$graph"
[[ $(deps --diff HEAD) == "$no_change" ]] || fail "header-only graph diff"
cp "$work/before.md" "$graph"
if deps --diff refs/heads/unknown-deps-test-revision >/dev/null 2>&1; then
  fail "--diff accepted an unknown revision"
fi
echo "PASS unchanged graph, header-only change and unknown revision"

# A new edge: a package that does not import internal/kernel yet does.
source=internal/web/i18n
edge="$source -> internal/kernel"
grep -qxF "$edge" "$graph" && fail "test setup: $edge already exists"
name=$(cd "$copy" && go list -f '{{.Name}}' "./$source")
printf 'package %s\n\nimport _ "%s/internal/kernel"\n' "$name" "$module" >"$copy/$source/zz_new_edge.go"
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

# Exercise the file comparison directly, including the easily confused
# internal/web and web/static units. Keep the added crossing edge above.
removed_edge='cmd/ribbitto -> internal/web'
grep -qxF "$removed_edge" "$graph" || fail "test setup: missing $removed_edge"
awk -v edge="$removed_edge" '$0 != edge' "$graph" > "$work/changed.md"
cp "$work/changed.md" "$graph"
cat >> "$graph" <<'EDGES'
```text
internal/web/i18n -> internal/web/middleware
internal/web/i18n -> web/static
cmd/ribbitto -> cmd/seed
cmd/ribbitto -> cmd/ribbitto/helper
```
EDGES
cat > "$work/expected.md" <<'EXPECTED'
## Package-import edge changes

### Crossing units

- Added: `cmd/ribbitto -> cmd/seed`
- Added: `internal/web/i18n -> internal/kernel`
- Added: `internal/web/i18n -> web/static`
- Removed: `cmd/ribbitto -> internal/web`

### Within one unit

- Added: `cmd/ribbitto -> cmd/ribbitto/helper`
- Added: `internal/web/i18n -> internal/web/middleware`
EXPECTED
deps --diff HEAD > "$work/actual.md"
diff -u "$work/expected.md" "$work/actual.md" || fail "changed edges or unit groups differ"
echo "PASS added crossing, within-unit and outside edges; removed edge; crossing first"

cp "$work/before.md" "$graph"
deps --diff HEAD^ > "$work/actual.md"
count=0
while IFS= read -r line; do
  [[ $line == *' -> '* ]] || continue
  grep -qxF -- "- Added: \`$line\`" "$work/actual.md" || fail "missing added edge: $line"
  count=$((count + 1))
done < "$graph"
[[ $(grep -c '^- Added:' "$work/actual.md") == "$count" ]] || fail "extra added edges"
if grep -q '^- Removed:' "$work/actual.md"; then
  fail "removed edge when the base has no graph"
fi
echo "PASS every edge is added when a valid revision has no graph"
