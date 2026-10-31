# 44. Remove the retired launcher language-list entries

**Proposed through** [#2005](https://github.com/tkakkie/ribbitto/issues/2005),
following the common AI workflow in #2002. This supersedes only decision
42's allowances for the retired launcher's guard JSON/JSONL fixtures and
its verification-image Dockerfile. The maintainer's approval of this
amendment is required on #2005 before merge.

Remove those exact-path entries from `tools/languagecheck` with the launcher.
There is no remaining allowance for a verification-image recipe or those
guard fixtures. All other language-list entries, testdata fences, approval
requirements and exception checks in
[decision 42](42-repository-languages-and-formats.md#language-list) and
[decision 43](43-typescript-with-a-go-driven-build.md) remain in force.

`tools/languagecheck` uses an explicit Go path-and-kind table, not a parser
of the decision's prose. Keep that table and its tests consistent with this
amendment. Historical decision 42 remains a record; its obsolete browser
update link is plain text so documentation checks do not follow it.

**Why:** the code, image recipe, fixtures, documentation and CI shards are
removed together. Keeping a language allowance after its sole use is gone
would admit new files without a current product reason.

**Considered:** retaining the entries (a stale allowance); editing decision
42's language list in place (would obscure the historical approval).
