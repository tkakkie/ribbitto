# Budgeted dev and load containers

The budget and `budget-start` are merged but **NOT in force**; this is not approval to start, provision or enable anything.
See the [start procedure](argon-verify-budget-start.md#start-procedure).

## Development PostgreSQL

The maintainer's harness supplies a `compose.budget.yml` override and wrapper;
neither lives in this repository. `Makefile` and `compose.yml` stay unchanged.
The override registers PostgreSQL with `argonreview.budget=dev`,
`argonreview.slot=2`, `mem_limit: 1g` and `memswap_limit: 1g`.
It consumes a whole 2 GiB unit despite its smaller limit.

The wrapper runs `docker compose -f compose.yml -f <override> create postgres`,
obtains its full ID, calls `budget-start --container <ID>`, then waits for
health. Any failure stops the procedure. The wrapper is the maintainer's harness
outside N3's tooling cases; its cleanup is the maintainer's and happens
[at once](argon-verify-budget-start.md#create-just-before-starting), giving
Claude and the tooling no removal authority.
Run it only while used; stop with
`make db-down`. Running `make db-up` by habit recreates it unlabelled: discovery
blocks all new verifications until the wrapper runs again. A plain `docker run`
Codex database is likewise unregistered and blocks checks.

## Load and measurement containers

Create load and measurement containers with `argonreview.budget=exclusive`
and no slot label, then start their full IDs through `budget-start`. They pause
new verification; [measurement rules](investigations.md#host-measurements)
still require workers to stop before measurements. Removing the throwaway
measurement database is Claude's own cleanup by recorded full ID under
[investigations step 9](investigations.md#host-measurements), outside the
[tooling table](argon-verify-budget-start.md#stop-and-removal-authority);
no tool here removes it. The budget is not measurement approval.
