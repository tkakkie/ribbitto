# Argon browser call arguments

**Merged but not in force:** nothing here enables or authorizes anything.
See [browser operation](argon-verify-browser.md).

## Call arguments

The `verify` schema includes `test-browser` and a string `run` property.
With `--allow-verify`, the guard uses `parseVerifyArguments`; MCP uses the
same parser and dispatches to `verifyBrowser`. `container-check` accepts
`<worktree> test-browser <package> <run> [--browser-mode <abs path>]`;
both paths reach `runContainerExecution`.

For the `test-browser` call, require `check`, `package` and `run`.
These are its acceptance rules; they do not authorize a call before enabling.
`package` is exactly `./internal/web` or `./cmd/ribbitto`.
`run` is required for this check only and refused for other checks.
Require exactly those three case-sensitive keys, string values, no extra
or duplicate key (including escaped spellings), missing value, `null` or number.

- The run is 1–200 bytes, using only ASCII letters, digits, `_`, `=`, `-`
  and `/`. No regexp metacharacters, `#`, comma, colon, quotes, percent,
  whitespace, NUL or non-ASCII byte is accepted.
- There is one top-level segment and at most six subtest segments, none
  empty; each segment is at most 64 bytes.
- The top level is 5–64 bytes of letters, digits or `_`, starts with `Test`,
  has a fifth byte that is not lowercase, and ends in `Browser`.
  `TestBrowser` alone is refused.
- Subtest segments start alphanumeric and use letters, digits, `_`, `=`
  or `-`. Each is 1–64 bytes.
- Each segment becomes `^segment$` in `-run`, so prefixes do not match.

Examples accepted by the validator are
`TestBrowser/drafts-markers/sidebar-sse`,
`TestBrowser/quote/scripts=true/width=1280` and `TestTopicPostBrowser`.
`validateContainerCheck` validates package and run on the common path,
including direct `container-check` and MCP calls. Invalid values are refused
before admission, any switch/gate read, reservation, copy or container.
`container-check` prints usage to stderr and exits 2; MCP returns JSON-RPC
error `-32602` (`invalid verify arguments`). The package validator's error
is `browser package is not allowed`; run errors identify the violated rule.
