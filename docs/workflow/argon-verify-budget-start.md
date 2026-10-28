# Budgeted host container starts

The shared budget, `budget-start` and these procedures are merged, **NOT in
force** until a separately approved launcher update. Production still uses
`argonreview-2e3a6bf`. This is not approval to start, provision or enable
anything. All [enabling preconditions](argon-verify-budget.md#host-lock-provisioning),
combined security review and preflight of [#1908](https://github.com/tkakkie/ribbitto/issues/1908)
still apply, including to both workers and host commands.

## Before enabling

Until `budget-start` is in force through the separately approved launcher
update, Claude uses this current interim procedure for a needed Codex database:

```sh
if ! docker ps --format '{{.Names}}' | grep -qx ribbitto-codex-pg; then
  docker run -d --name ribbitto-codex-pg -p 127.0.0.1:55433:5432 \
    -e POSTGRES_PASSWORD=codex-dev-only postgres:18
fi &&
(
  for i in $(seq 1 30); do
    docker exec ribbitto-codex-pg pg_isready -U postgres >/dev/null 2>&1 && exit 0
    sleep 1
  done
  echo "ribbitto-codex-pg: PostgreSQL not ready after 30 tries" >&2
  exit 1
)
```

Start Codex only if this exits 0. A stopped container with that name
makes `docker run` fail; remove it with `docker rm -f ribbitto-codex-pg`,
also used for cleanup. If port 55433 is taken, change it in both commands.

Measurement step 8 of [host measurements](investigations.md#host-measurements)
is met without `exclusive`/`budget-start` until then; all its current conditions
still apply. All following sections apply once in force.

## Start procedure

See [development PostgreSQL and load containers](argon-verify-budget-dev.md).

The host-only command is `argonreview budget-start --container <full 64-hex ID>`.
It accepts exactly that option and a full lowercase hexadecimal ID: no command,
image, slot override or extra argument. It is never offered to Argon through
the guard, schema, MCP or implementer wiring. After argument validation it
requires the same trusted-build check as [container-sweep](argon-verify-sweep.md).

1. Inspect the exact ID. Require state `created`; refuse a verification name
   (`argonreview-check-` plus 32 lowercase hex digits) or the presence of
   `argonreview.owner`, even with an empty value. Require one of:
   - `argonreview.budget=dev`, a strictly ascending one- or two-slot
     `argonreview.slot` label using slots 0–2, positive memory, equal memory
     and swap, and memory at most slots times 2 GiB. Reuse the reconciler's
     parser and M2's registration rules.
   - `argonreview.budget=exclusive`, with no slot label at all.
2. Derive locks only from those inspected labels. Dev takes its slots;
   exclusive takes `browser.lock` and all three slots. Use the shared lock
   primitive and order: browser, slot 0, 1, 2, all or none.
3. Wait at most **2 minutes for dev**, **15 minutes for exclusive**, under
   one deadline. Every attempt is nonblocking, releases the entire set before
   sleeping one poll, and holds nothing while waiting.
4. Under all locks, discover processes before reading a fresh full snapshot.
   Require the target exactly once
   with the same ID, state `created`, name, image, labels, memory and swap as
   the first inspection. Exclude only that ID and feed the remaining snapshot
   to the production slot reconciler, process table and M2 hook. Any unavailable
   cause refuses immediately: unknown state, discovery error, orphan, mismatch,
   unregistered/unlimited/over-limit container, duplicate or other invalid
   reservation. An exclusive pause or any held target slot is retried until
   the deadline, then refused as busy. Exclusive needs every slot free.
   Verification leftovers retain their slots; this command never sweeps them.
5. Run `docker start <that exact ID>`, never its name.
6. Inspect again with a fresh bounded context independent of the wait deadline
   or caller cancellation. Require the expected running container with unchanged
   identity, labels, memory and swap. Inspect even if start returned an error.
   If start returned an error, or inspection cannot establish the expected
   running container, issue only `docker stop --time 2 <that ID>`, once, and
   refuse. A failed stop may leave it running.
   Report `stop failed` distinctly if stopping fails; exit 1. Never remove,
   kill, pause or update it.
7. Release all locks and print only the validated ID plus newline on stdout.

Exit 0 means started; 1 means refused or failed; 2 means usage. Refusal stdout
is empty. Diagnostics name checks with fixed text only, never Docker-controlled
names, labels, errors or output. Read queries use the read-only inventory.
If writing the ID line fails after a successful start, the exit is 1 but the
container may be running: `budget-start` does not stop it. The caller must
`docker inspect` the recorded ID and decide, never assuming it stopped.
A separate mutating adapter permits **only** `start <valid ID>` and
`stop --time 2 <valid ID>`, with the runner's fixed environment, output cap
and per-call time limit; no other verb or argument shape reaches Docker.

This closes the create/start race: a created dev container already holds its
labelled slots in reconciliation, and verification runners re-read the world
under their chosen locks before using a reservation. Every starter must use
this procedure; the [limits](#limits) still apply.

## Create just before starting

Create the dev or Codex database container immediately before `budget-start`,
never earlier: a created container already holds its labelled slots.
If recording its ID, `budget-start` or the readiness wait fails, clean up
**at once**, with no waiting or manual leftovers.
For the Codex database, this is case 5's interruption: no Codex process has
started. Stop and remove only the ID `docker create` returned, after the
[recorded-ID Codex database lifecycle](#codex-database-lifecycle) inspection
of name, both labels and image. If recording failed, inspect that returned
ID, never the name. If no ID was returned or it cannot be confirmed, remove
nothing (never by name or label, never guess an ID); list and report, and
stop for the maintainer under the lifecycle's unknown-ID rule.
The development database wrapper is the maintainer's harness outside N3's
tooling cases; its cleanup is the maintainer's and grants Claude and the
tooling no removal authority.
A created container claiming a slot a verification already holds
makes every caller get `unavailable: a slot is claimed twice` until one of
them ends; a transient message of that kind can come from this.
The code is deliberately unchanged: N2 counts `created`.

## Stop and removal authority

N3 permits only these five tooling cases; labels alone never grant removal authority.

| Case | Who | Target and ownership | Allowed when |
|---|---|---|---|
| Own cleanup | Verification runner | The random 128-bit name it gave its own `docker run` | Its own run ending: `stop --time 2`, `rm -f <name>`, removal confirmed before release. A failed removal leaves the label holding the slot. |
| Leftover reclaim | Trusted sweep | Full ID with exact name, owner label and pinned image, all checked twice | `live()` false (over 90 s, no non-orphan client, copy unlocked), checked twice: `rm -f <ID>`, absence confirmed; otherwise `unreclaimed`. [All sweep conditions](argon-verify-sweep.md#scope-and-order) apply. |
| Emergency stop (B9) | Claude | Full ID with owner and browser-class labels; inspect confirms name, labels and image; it may be live | Only on a B9 trigger after `browser-mode off`: `stop`, `rm -f <ID>`, absence confirmed, recorded. |
| Failed start | `budget-start` | The ID it started in this call, seconds earlier | Only if `start` errored or the post-start inspection differs: one `stop --time 2`, never removal. |
| Codex database | Claude | The recorded full ID; inspect confirms name, both labels and `postgres:18` | After use or interruption, once no Codex process remains ([lifecycle](#codex-database-lifecycle)). |

## Codex database lifecycle

N4 allows `ribbitto-codex-pg` **only while a Codex implementation needs database
tests**, never for reviews or database-free work. Claude owns its lifecycle;
Argon never starts, stops or deletes it, and Codex cannot use Docker.
Before enabling the budget, remove any old unregistered `ribbitto-codex-pg`.

For a qualifying run, Claude creates it with exactly:

```sh
docker create --name ribbitto-codex-pg --label argonreview.budget=dev --label argonreview.slot=1 --memory 1g --memory-swap 1g -p 127.0.0.1:55433:5432 -e POSTGRES_PASSWORD=codex-dev-only postgres:18
```

Record the returned full ID, time and issue in the run's handoff log directory
as `codex-pg.id`. Call the trusted `argonreview budget-start --container <ID>`.
Then wait for readiness using that recorded ID:

```sh
id=$(cat codex-pg.id)  # recorded ID, verified as below
(
  for i in $(seq 1 30); do
    docker exec "$id" pg_isready -U postgres >/dev/null 2>&1 && exit 0
    sleep 1
  done
  echo "Codex PostgreSQL not ready after 30 tries" >&2
  exit 1
)
```

Set `id` from the record before the loop, only after confirming it equals the
64-hex ID `docker create` returned and that `docker inspect` of it shows the
expected name, both labels and image. Start Codex
only if creation, recording, `budget-start` and this 30-try wait all exit 0.
Follow [Create just before starting](#create-just-before-starting).
The implementation brief supplies port **55433** and the test environment
[values](running-other-ai.md#implementation-brief). If the port is taken,
change it in creation and the test URL together; no fallback database.
A stopped same-name container is not reused: investigate and apply recorded-ID
cleanup before creating another.

At run end, Claude confirms no Codex process remains, inspects the recorded
ID and requires name `/ribbitto-codex-pg`, `argonreview.budget=dev`,
`argonreview.slot=1` and image `postgres:18`. Stop and remove **that ID**, then
confirm it is absent. Never clean up by reusable name.

After interruption, **before any next work**, Claude reclaims a matching
recorded container by the same checks and procedure. An unknown ID, an
unregistered container or a live Codex process means no new work until
investigated. Do not guess an ID or remove an unrelated container.
Both databases at once consume two units, leaving only one; browser calls
need two units and are busy.

## Limits

Start every container through `budget-start`; never `docker update` a registered
one; stop workers before a measurement. Unregistered, unlimited, over-limit
and exclusive containers block **NEW runs only**. Detection cannot guarantee
against a container started outside `budget-start` during a run, plain
`docker start` of a labelled container, `docker update` between polls, other
engines or VM memory. An out-of-memory kill is never verified.
The budget holds only once every container-starting caller runs the updated
binary, which the single, separately approved launcher update enforces.
