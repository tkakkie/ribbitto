# Argon browser preflight probes

**Merged but not in force:** nothing here enables or authorizes anything.
See [preflight and approvals](argon-verify-browser-preflight.md).

## Numbered browser probes

Run probes only in the approved preflight window via the candidate's
`test-browser` call. Under #1908 Approvals 3, switch states use the
temporary two-home swap: probes 1–5 need `browser-mode` `on` and a fresh gate;
probe 6 varies them. The host gate is fixed at
`/var/cache/argonreview/browser-security-check` (`browserGatePath` in
`cmd/argonreview/browser_gate.go`), outside the swap.
At the start of the window, before probe 1, save a gate copy (bytes, mode,
link count) or record its absence.
For expired, future and missing states, manual replace/remove is allowed
only in that window. Window authority alone permits it only if the gate
was absent at window start: create by hand and remove again. Otherwise,
separate maintainer deletion approval is required first, including for any
real `--init` gate.
Before window end, restore production: `browser-mode` `off`,
the production gate restored from the saved copy (or absent as before).
If absent at the start, verify at window end that the gate does not exist
and record that absence; a hand-made fresh gate from the probes must not
survive as a stand-in for the weekly record and `--init`.
#1700 restoration evidence must show bytes, mode, link count (or absence)
match the saved state and the switch reads `off`. See
[recording](argon-verify-browser-record.md#initial-and-routine-recording)
(path changes need approval to remove the gate).
Approval 3 never authorizes production `browser-mode on`: approval 6 follows
launcher update (4) and stage B enabling (5). Passing authorizes nothing else
(#1908 Approvals 3–6; Enabling sequence 6–8).

In the window, before the probes, record the `docker ps -a` count and time
one reservation. Budget inventory runs `docker inspect` on every container,
including exited ones, under a 30 s inventory ceiling inside a 120 s
admission deadline. If the reservation, timed with no holders running,
takes more than **10 s** (one third of the 30 s inventory ceiling; maintainer
decision, 2026-10-28), or consistency cannot be confirmed (inventory could
not be determined, or a check fails closed with the "could not be determined"
refusal), the preflight is **NO-GO**.

Require determinism; timing-dependent contention never passes.
Argon reads each result inline. Keep stage A's refusal, smoke, restoration
and evidence requirements from
[extended preflight](argon-verify.md#extended-preflight) and
[#1700's stage A obligations](argon-workers-operation.md#updates-and-capacity).

1. **Success:** require `verified` for `TestBrowser/htmx-load-count`,
   `TestBrowser/drafts-markers/sidebar-sse` and
   `TestBrowser/quote/scripts=true/width=1280` in `./internal/web`, and
   `TestTopicPostBrowser` in `./cmd/ribbitto`.
2. **Failure/skip/nothing run:** a broken template gives `FAILED`; `t.Skip`
   gives `NOT VERIFIED`; `TestBrowser/no-such-scenario` and
   `TestBrowser/quote-selection` give “did not run”. Record each.
3. **Limits:** sleeping 11 minutes gives
   `NOT VERIFIED: time limit exceeded`; more than 4 GiB or more than
   2,048 threads is never `verified`. Then require no container,
   source copy or held slot.
   For cap/kill observations, attribution to our own cancellation is
   best-effort: a successful process-group kill cannot distinguish an
   independent SIGKILL racing our cap or malformed stop. See the
   [accepted residual risk](argon-verify-testdb.md#result-summary) and
   [N1 precedence](argon-verify-aggregation.md#n1-verdict-precedence).
4. **Controlled holders:** use dummy dev containers on the pinned verify
   image, `--network none --read-only`, 1 GiB, one slot each, `sleep 900`,
   started through `budget-start`; use a 300 s browser test to hold
   `browser.lock`. With slots 1 and 2 held, require `test` `verified` and
   `test-browser` busy after **120 s ± 5 s**. With all three
   slots held, a 1-unit call is busy. With the browser holder running,
   a second `test-browser` is busy. Remove a holder 30 s into a wait: require
   `verified` and a logged wait of at least 30 s. With no holders,
   all three 1-unit calls are `verified`. A `SIGKILL`ed runner's leftover
   blocks its slot until `container-sweep`; after that, a call is
   `verified`. A mismatched verification-named container fails closed.
5. **Other containers:** an unlabelled container or a dev container without a limit
   fails closed and stays untouched; after removal the next call
   is `verified` with no approval. `make db-up` fails closed until the wrapper
   runs again. A created exclusive container pauses. Exclusive
   `budget-start` waits out the browser holder; `budget-start` refuses a
   running container, wrong ID or wrong labels. Run one full Codex database
   lifecycle with its recorded-ID and process checks.
6. **Switch/gate:** follow the
   [swap and gate save/restore rule](argon-verify-browser-probes.md#numbered-browser-probes).
   “Replace/remove” never applies to a real `--init` gate without separate
   maintainer deletion approval.
   `off`, missing or malformed switches from worker 1,
   worker 2 and `container-check` give `NOT VERIFIED: browser check paused`
   without starting a container. With the switch `on`, expired, future or
   missing gate entries give
   `NOT VERIFIED: browser check paused: security check overdue`.
   `browser-check-record` refuses expired refresh with the switch `on`.
   The [emergency pause](argon-verify-browser.md#removal-authority-and-emergency-pause)
   leaves no browser container or copy, and every slot free.
7. **Refusals through Argon:** exercise the
   [run-validator rows](../../cmd/argonreview/container_browser_test.go)
   that are typable, plus stage A's refusal/smoke set. Replace only
   its “`test-browser` is an unknown check” row with guard admission followed
   by `NOT VERIFIED: browser check paused` when `browser-mode` is `off`.
   Keep all other refusals and absent effects.
   The guard checks argument shape only; `parseVerifyArguments` also applies
   package/run validators. It reads no switch or gate and never starts a
   container; admission is the runner's check.
8. **Inside the container:** inspect network `none`, read-only root, no
   capabilities, `no-new-privileges`, uid 1000, no seccomp option, pinned
   image and labels. Chrome runs as uid 1000 with `--no-sandbox`. Loading
   host listeners on loopback, `host.docker.internal`, 192.168.65.x,
   172.17.0.1 and the LAN address must log no request; a default-network
   control container must log requests.
