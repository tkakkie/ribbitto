# Verify log MCP tool

The read tool from #1913 PR 2b is merged, off by default and unexposed.
Only a server started with trailing `verify-mcp --logs` serves it. The guard,
`hooks.json`, settings generators, harness switch and wrappers do not admit it
until PR 2c; Argon cannot reach it in production. The
[design and provisional limits](argon-verify-log.md) still govern retention,
selection, framing, taint, budgets and later preflight. No enabling is implied.

## Listing and arguments

With logs on, tools/list lists `verify`, then `verify-log`. Logs off lists
only `verify`, creates no store and refuses a log call exactly like an unknown
tool: `-32602 "invalid verify arguments"`, without a read audit. The complete
verify entry, description and schema retain the base commit's bytes.

`verifyLogInputSchema` in
`cmd/argonreview/verifylog_schema.go` describes exactly four string properties,
with `additionalProperties: false`. Required: `run`, `stream`, `start`.

- `run`: exactly the lowercase hexadecimal capture ID, its width derived
  from `verifyLogIDBytes`.
- `stream`: `index`, `test`, `package`, `stderr`, `output`.
- `name`: required only for test/package, forbidden otherwise; an ordinal `#N`
  or exact unique display name. The description states the conditional rule,
  ordinal syntax, UTF-8 byte limit and invisible-character restrictions.
- `start`: ASCII digits, first nonzero, width from `verifyLogStartDigits`.
  The reader, rather than the stateless parser, checks the line count.

All limits derive from `cmd/argonreview/verifylog_limits.go`. The schema is
advisory: JSON Schema counts characters for maxLength, while the existing
`parseVerifyLogArguments` enforces the stricter UTF-8 byte limit and every
conditional rule. No argument is a path. A path-shaped run/start is malformed;
a path-shaped display selector is looked up by equality and normally not listed.
It never opens that path.

## Serving and audit

The single response-writing loop in `cmd/argonreview/verifymcp.go` dispatches
reads only after initialization and only when its store exists. Reads use
completed captures from that process. They bypass worktree validation and the
verify busy check, start no verifier/container, and do not wait for an in-flight
verify. No read goroutine is added.

`cmd/argonreview/verifylog_call.go` validates only name, arguments and optional
object `_meta`, then delegates to the merged reader. Malformed arguments or
call params go through the store's malformed class, with its counters and one
audit, returning `-32602 "invalid verify arguments"` with the request ID. The
refusal table owns this exact protocol text. Unknown tools keep the old refusal.

The envelope parser checks duplicate keys recursively. For identifiable log
calls rejected there, the adapter temporarily replaces the params object with
an empty object and reuses the same envelope parser. It restores the original
params bytes for strict validation by the store. Duplicate envelope keys,
invalid IDs and invalid JSON remain envelope errors; notifications remain
unanswered. Verify's parsing and errors are unchanged.

The store returns a serialized response line plus one audit string. The loop
writes the audit to stderr and the exact line to stdout, without re-encoding.
The raw writer treats a short write or error like the existing response writer:
cancel the in-flight verify, shut down and exit 1. The audit has at most
`verifyLogAuditIDDigits` ID digits and no name, match or content. Serialized
line bytes, including IDs, envelopes and refusals, spend the reader's budget.

## Regression evidence

`cmd/argonreview/verifylog_wire_test.go` exercises the server loop with synthetic
records and recording fake verifiers; it needs neither Docker nor a database.
Named tests cover:

| Tests (prefix `TestVerifyLogWire`) | Contract |
|---|---|
| `Listing`, `Schema`, `VerifyRegression` | On/off listing, byte-equal verify entry, schema equality, derived checks and shared guard cases, parser separation |
| `StrictArguments`, `Envelope`, `SharedRefusal` | Exact malformed protocol bytes/audit, envelope protection, initialization, notifications, unknown/evicted/other-process IDs |
| `CaptureBudgets`, `ServerReadBudget`, `ServerByteBudget` | Shared per-capture budgets, server reads/serialized bytes, refusal accounting |
| `Framing`, `TaintAndAudit` | Wire/line caps, maximal ID, nonce freshness, forged output, sanitization, whole-unit taint and withheld-name selection |
| `Stderr` | Reads following actual verify calls: runner failure, timeout and cancellation denied; ordinary failure serves only the prefix |
| `NoWaitOrContainer`, `NoFiles`, `WriteFailure` | Completed reads while verify blocks, no verifier/worktree call, path-shaped selectors, raw-write cancellation |
| `SummaryOrdinals` | Shared complete/incomplete/skip records: printed ordinals and run ID agree with wire index and test/package selection |

The existing `TestVerifyLogReadImportBoundary` inspects every production
`verifylog_*.go` file with a closed import/direct-call set. The adapter adds only
the existing pure envelope parser to that call set; no imports are added to the
allowed set. An `os` import mutation in a new reader file must fail it. Production
mutations and the tests that reject them are recorded in the pull request body.
