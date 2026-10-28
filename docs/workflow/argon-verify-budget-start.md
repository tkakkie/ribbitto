# Budgeted host container starts

The shared budget, `budget-start` and these procedures are merged, **NOT in
force** until a separately approved launcher update. Production still uses
`argonreview-2e3a6bf`. This is not approval to start, provision or enable
anything. All [enabling preconditions](argon-verify-budget.md#host-lock-provisioning),
combined security review and preflight of [#1908](https://github.com/tkakkie/ribbitto/issues/1908)
still apply, including to both workers and host commands.

## Start procedure

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
6. Inspect again, requiring the expected running container with unchanged
   identity, labels, memory and swap. Inspect even if start returned an error.
   If start returned an error, or inspection cannot establish the expected
   running container, issue only `docker stop --time 2 <that ID>`, once, and
   refuse: the ID started by this call is never left running on failure.
   Stop failure still refuses; never remove, kill, pause or update it.
7. Release all locks and print only the validated ID plus newline on stdout.

Exit 0 means started; 1 means refused or failed; 2 means usage. Refusal stdout
is empty. Diagnostics name checks with fixed text only, never Docker-controlled
names, labels, errors or output. Read queries use the read-only inventory.
A separate mutating adapter permits **only** `start <valid ID>` and
`stop --time 2 <valid ID>`, with the runner's fixed environment, output cap
and per-call time limit; no other verb or argument shape reaches Docker.

This closes the create/start race: a created dev container already holds its
labelled slots in reconciliation, and verification runners re-read the world
under their chosen locks before using a reservation. Every starter must use
this procedure; the [limits](#limits) still apply.

## Stop and removal authority

N3 permits only these five cases; a label alone never grants removal authority.

| Case | Who | Target and ownership | Allowed when |
|---|---|---|---|
| Own cleanup | Verification runner | The random 128-bit name it gave its own `docker run` | Its own run ending: `stop --time 2`, `rm -f <name>`, removal confirmed before release. A failed removal leaves the label holding the slot. |
| Leftover reclaim | Trusted sweep | Full ID with exact name, owner label and pinned image, all checked twice | `live()` false (over 90 s, no non-orphan client, copy unlocked), checked twice: `rm -f <ID>`, absence confirmed; otherwise `unreclaimed`. [All sweep conditions](argon-verify-sweep.md#scope-and-order) apply. |
| Emergency stop (B9) | Claude | Full ID with owner and browser-class labels; inspect confirms name, labels and image; it may be live | Only on a B9 trigger after `browser-mode off`: `stop`, `rm -f <ID>`, absence confirmed, recorded. |
| Failed start | `budget-start` | The ID it started in this call, seconds earlier | Only if `start` errored or the post-start inspection differs: one `stop --time 2`, never removal. |
| Codex database | Claude | The recorded full ID; inspect confirms name, both labels and `postgres:18` | After use or interruption, once no Codex process remains ([lifecycle](#codex-database-lifecycle)). |

## Development PostgreSQL

The maintainer's harness supplies a `compose.budget.yml` override and wrapper;
neither lives in this repository. `Makefile` and `compose.yml` stay unchanged.
The override registers PostgreSQL with `argonreview.budget=dev`,
`argonreview.slot=2`, `mem_limit: 1g` and `memswap_limit: 1g`.
It consumes a whole 2 GiB unit despite its smaller limit.

The wrapper runs `docker compose -f compose.yml -f <override> create postgres`,
obtains its full ID, calls `budget-start --container <ID>`, then waits for
health. Any failure stops the procedure. Run it only while used; stop with
`make db-down`. Running `make db-up` by habit recreates it unlabelled: discovery
blocks all new verifications until the wrapper runs again. A plain `docker run`
Codex database is likewise unregistered and blocks checks.

## Load and measurement containers

Create load and measurement containers with `argonreview.budget=exclusive`
and no slot label, then start their full IDs through `budget-start`. They pause
new verification; [measurement rules](investigations.md#host-measurements)
still require workers to stop before measurements. Remove throwaway measurement
containers afterwards as before. Do not use the budget as measurement approval.

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
(
  for i in $(seq 1 30); do
    docker exec "$id" pg_isready -U postgres >/dev/null 2>&1 && exit 0
    sleep 1
  done
  echo "Codex PostgreSQL not ready after 30 tries" >&2
  exit 1
)
```

Here `id` is the validated full ID from creation and the record. Start Codex
only if creation, recording, `budget-start` and this 30-try wait all exit 0.
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
need two units and are busy. This does not enable browser checks.

## Limits

Start every container through `budget-start`; never `docker update` a registered
one; stop workers before a measurement. Unregistered, unlimited, over-limit
and exclusive containers block **NEW runs only**. Detection cannot guarantee
against a container started outside `budget-start` during a run, plain
`docker start` of a labelled container, `docker update` between polls, other
engines or VM memory. An out-of-memory kill is never verified.
The budget holds only once every container-starting caller runs the updated
binary, which the single, separately approved launcher update enforces.
