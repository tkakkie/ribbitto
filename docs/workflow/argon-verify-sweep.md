# Verification leftover sweep

The trusted launcher provides `argonreview container-sweep [--dry-run]`.
Set `RIBBITTO_ARGON_SOURCE` to the trusted source checkout, as for
[container verification](argon-verify.md#trusted-server). The binary must
match its clean trusted revision. No worktree or image argument is accepted.
`verify-mcp` and `container-check` also sweep before starting, after trust
checks; their sweep reports go to stderr. An unsuccessful sweep refuses
startup. The standalone command reports on stdout, with names, IDs and paths
only; diagnostics name the failed step without Docker output or file contents.

## Scope and order

Reclamation handles orphaned Docker clients first, containers second, source
copies last. It never prunes Docker, follows a copy symlink or removes an unvalidated
container. Orphan signals target exact PIDs. Docker uses the runner's fixed
executable path, local socket and environment, ignoring inherited Docker
configuration.

- Clients must belong to this uid, have parent PID 1, and match the runner's
  absolute Docker executable and fixed run/name/network command prefix.
  Re-read PID, start time, uid, parent and exact command before each signal;
  send TERM, wait up to five seconds, then KILL and wait again.
- Containers need the exact lowercase 32-hex verification name,
  `argonreview.owner=verify` label and pinned Go image. Inspect again before
  removing by full 64-hex ID, then confirm absence. A matching name and label
  with a different image reports an unreclaimed container without removal.
- Copies must be direct children of the runner's fixed `/tmp` parent, named
  `argonreview-container-` followed by 1–20 digits, real directories owned by
  this uid with exactly mode 0700. `/tmp` is resolved once (including macOS's
  symlink); `TMPDIR` cannot redirect either the runner or sweep. All containers,
  including stopped and unrelated ones, are inspected for mounts equal to,
  inside or above a copy, comparing cleaned lexical paths and resolved paths.
  Any source component equal to the unique copy name also protects it.
  Docker Desktop prefixes are handled by removing leading components and
  comparing each nonempty remainder with the copy's real and `/tmp` paths;
  prefixed ancestor mounts conservatively protect copies too. Missing sources
  (ENOENT or ENOTDIR), including VM volume paths, retain these comparisons.
  An empty bind source, relative path, NUL, `..` component or other resolution
  error leaves the copy unreclaimed and fails the sweep. An absolute named-volume
  source elsewhere does not block removal. A failed inspection is ignored only if a fresh
  listing confirms that ID is gone; otherwise discovery failure prevents deletion.
  Recursive removal uses directory descriptors and never crosses devices,
  including between the resolved parent and copy root. It reads all entries
  before unlinking, reopening for full passes until empty, at most eight passes
  per directory. Unsafe entries, mount boundaries or exhausted passes fail the sweep.
  Concurrent disappearance is accepted: failed container removal needs a fresh
  absence listing; ENOENT during copy removal needs a confirming descriptor-relative
  stat. A copy gone before opening is ignored. Other errors remain failures.

## Liveness

The runner locks its private copy directory with exclusive `flock` immediately
after creation. It holds the descriptor through source removal, then closes
it. Close-on-exec prevents the Docker child inheriting the lock; server death,
including SIGKILL, releases it. The sweep holds a successful nonblocking
exclusive lock probe through removal, also excluding concurrent sweepers.

A locked copy or a copy modified less than 90 seconds ago is in use. The copy
grace counts from the copy directory's mtime (when `tree` is created), covering
creation before locking. A server killed within 90 seconds leaves its leftovers
for a later sweep. A run by an older, pre-flock binary is protected only by its
mount. A container is in use if its `/work` bind
source's copy parent is live, a client of this uid matches its name, or its
creation is less than 90 seconds old. Each predicate alone protects it.
Live or mounted entries report `skipped (in use)` and are not errors.

## Status and limits

Exit 0 means no unreclaimed leftovers or discovery errors; exit 1 means an
error or unreclaimed leftover. `--dry-run` changes nothing, and exits 1 when
it detects any leftover, 0 when clean. Invalid arguments exit 2.

SIGKILL cannot trigger cleanup: reclamation happens on the next trusted sweep,
not at a hard lifetime deadline. A live run is protected regardless of age.
Containers made before the ownership label was added are never reclaimed;
inspect them manually and use `docker rm -f <id>` with their exact ID.
Their mounted copies remain protected. The sweep can be called independently
before and after checks; the shell launcher does not yet add those calls.

## Host preflight

Before relying on the sweep, the #1433 preflight on the host must run it once
with `--dry-run` against real Docker with a real labelled container. Fake tests
cannot confirm these real Docker CLI assumptions:

- `Config.Image` equals the pinned `golang@sha256:...` string exactly.
- The label and inspect JSON field names are `Id`, `Name`, `Created`,
  `Config.Image`, `Config.Labels` and `Mounts[].Type/Source/Destination`.
- Record the bind Source form of `/work` exactly as real inspection reports it,
  including any Docker Desktop prefix.
- Confirm macOS `ps -axww -o pid=,ppid=,uid=,lstart=,command=` output parses by
  running `container-sweep --dry-run` on the host: one unparsable line fails
  every sweep.
- Measure that the startup sweep finishes within agy's MCP start timeout.
  An orphan client can take up to about 10 seconds of TERM/KILL waits.

Record these preflight results, including the ownership label and mount
sources in that real container's inspection. This implementation has not run
Docker or the built sweep; host validation remains pending.
