# 47. Minimal Node declarations for client unit tests

**Decided** ([#2125](https://github.com/tkakkie/ribbitto/issues/2125) and
[#2124](https://github.com/tkakkie/ribbitto/issues/2124), approved by the
maintainer on 2026-11-03;
[approval](https://github.com/tkakkie/ribbitto/issues/2125#issuecomment-6602538749)).
This supersedes in part the **Node test declarations** bullet of
[decision 45](45-type-checked-client-unit-tests.md): its module list is
extended narrowly below. Decision 45 remains unchanged as history.

## Amendment

The hand-written `web/test/node-test.d.ts` may additionally declare exactly:

- `readFileSync(path: URL, encoding: "utf8"): string` from `node:fs`.
- `doesNotMatch(value: string, pattern: RegExp, message?: string): void` on
  `node:assert/strict`.

`@types/node` and wholesale Node typings stay out. Other `node:fs` members
or additional modules need another decision. Members of the two existing
modules, `node:test` and `node:assert/strict`, keep decision 45's rule:
declarations cover only what the tests use and are "corrected minimally"
when a member is missing or wrong. The `doesNotMatch` declaration is such a
minimal correction.

The forbidden-identifier source scan of `web/src/selection-quote.ts` stays
in the TypeScript harness with the same power; it is neither deleted nor
moved to a Go check.

**Why:** converting the selection-quote test to type-checked TypeScript in
#2124 needs `readFileSync` for its existing source scan and `doesNotMatch`
for its assertions. Decision 45 already permits the missing assertion
member, but its module list excludes `node:fs`. The narrow declaration
keeps the scan in the harness without admitting Node's full type surface.

**Considered:** the three options in #2124:

1. **Chosen:** allow the one `node:fs` member in the hand-written
   declarations, through this new decision rather than rewriting decision 45.
2. Keep the scan but move it into an existing Go check (would relocate the
   check and require proof of equivalence).
3. Convert mention-token now and leave selection-quote as `.mjs` until
   option 1 or 2 is decided (would defer its type checking).
