# Browser preflight and enabling

**Merged but not in force:** nothing here enables or authorizes anything. See
[operation](argon-verify-browser.md),
[security records](argon-verify-browser-record.md) and
[image updates](argon-verify-browser-updates.md).

## Separate approvals

After `ready`, separate fresh approvals, in order: each merge on maintainer
instruction; host image build in chat after listing downloads; preflight
window/temporary two-home swap; production launcher update (fresh date,
commit, procedure; past results are not approval); stage B enabling;
`browser-mode on`; any scheduled task (follow-up). Updates repeat
build/update approvals through the
[update path](argon-verify-browser-updates.md#update-path).

## Provisioning before enabling

Under host approvals, Claude prepares/records:

1. Follow [host lock provisioning](argon-verify-budget.md#host-lock-provisioning)
   exactly: `/var/cache/argonreview/budget/` and its parent
   `/var/cache/argonreview/` are real directories owned by all callers' uid,
   not group- or world-writable (e.g. 0700; uid-owned 0755 parent allowed).
   The runner never creates them. Every sandbox must permit file creation and locking there. Slot locks and
   `browser.lock` self-create at 0600 under the documented file checks.
2. Record a `docker ps -a` baseline with no unregistered container. Remove
   the old unregistered `ribbitto-codex-pg`; keep the
   [recorded-ID database lifecycle](argon-verify-budget-start.md#codex-database-lifecycle)
   thereafter. Start all later host containers through `budget-start`;
   use the [dev/exclusive procedures](argon-verify-budget-start.md#start-procedure).
3. Before switching, require no leftover from the **old launcher's**
   `container-sweep`. After A2, the new sweep cannot reclaim old-image
   leftovers; they fail every check closed. Never switch around them.
4. Use one new build for all callers: both workers' run/check wrappers and
   Claude's `container-check` and `container-generate`. Older callers take
   no slots; never mix launchers.
   Both workers/`container-check` pass worker 1's
   [browser switch path](argon-verify-browser.md#switch-and-admission-order).
5. Complete the first [weekly record](argon-verify-browser-updates.md#weekly-check-and-expiry)
   and run `browser-check-record --init` via the frozen
   wrapper with every [initialization check](argon-verify-browser-record.md#initial-and-routine-recording):
   regular uid-owned switch, uid-owned 0500 `implementer/`, regular
   `verify-mode` beside it. These check location, not switch content/mode;
   before `--init`, Claude or the host-side frozen wrapper with pinned
   binary hash must confirm `off`/0400 as operator rules.
   The gate directory holds `browser-security-check.lock` for writers.

## Chromium mask preflight

A2 requires merged B3a and passing `TestContainerArgumentsGolden` and
`TestContainerChromiumMasked`. These image-free argument tests do not replace
real-image preflight. If masking would weaken another boundary, stop and
report on #1908 **before A2**.

On the real image, `dpkg -L chromium` must show all Chromium launch
executables under `/usr/lib/chromium`, masked for every non-browser check by
`--tmpfs /usr/lib/chromium:ro,noexec,nosuid,nodev,size=1m`.
Only `test-browser` omits it. This tmpfs never writes to or makes the
read-only root writable. Argument tests reject later overlapping mounts
and browser environment/script overrides defeating the mask.

Before launcher update, verify that a non-browser check cannot launch
Chromium on that image; run one ordinary check with the mask. The go-rod
helpers skip missing/unlaunchable browsers without `RIBBITTO_REQUIRE_BROWSER=1`;
non-browser checks never set it. A `PATH` wrapper alone cannot prove its
masked binary launches. Record the skip/launch refusal; unit tests prove
neither timing nor a real browser run. The mask must be harmless with
`--read-only`, even without Chromium.
A launch-capable executable outside `/usr/lib/chromium` or a masked
non-browser check launching Chromium forbids launcher update: report on
#1908 before A2; nothing is enabled.

## Numbered browser probes

Run probes only in the approved preflight window via the candidate's
public `test-browser` call. Under #1908 Approvals 3, switch states use the
temporary two-home swap: probes 1–5 need `browser-mode` `on` and a fresh gate;
probe 6 varies them. The host gate is fixed at
`/var/cache/argonreview/browser-security-check` (`browserGatePath` in
`cmd/argonreview/browser_gate.go`), outside the swap.
Before probe 6, save a gate copy (bytes, mode, link count) or record absence
(normal before enabling). Manually replace/remove that path for expired,
future and missing states only in the approved window.
Before window end, restore production: `browser-mode` `off`,
the production gate restored from the saved copy (or absent as before).
#1700 restoration evidence must show bytes, mode, link count (or absence)
match the saved state and the switch reads `off`. Deleting a real production gate
(from `--init`) needs separate maintainer approval: see
[recording](argon-verify-browser-record.md#initial-and-routine-recording)
(path changes need approval to remove the gate). Only a gate absent before
the window may be removed again on window authority alone.
Approval 3 never authorizes production `browser-mode on`: approval 6 follows
launcher update (4) and stage B enabling (5). Passing authorizes nothing else
(#1908 Approvals 3–6; Enabling sequence 6–8).

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
6. **Switch/gate:** follow the swap and gate save/restore rule above.
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
8. **Inside the container:** inspect network `none`, read-only root, no
   capabilities, `no-new-privileges`, uid 1000, no seccomp option, pinned
   image and labels. Chrome runs as uid 1000 with `--no-sandbox`. Loading
   host listeners on loopback, `host.docker.internal`, 192.168.65.x,
   172.17.0.1 and the LAN address must log no request; a default-network
   control container must log requests.

## Enabling sequence and record

1. After all prerequisite PRs merge, Claude Opus (primary)
   and one Argon adversarial run review the entire integrated diff. Record
   each finding's severity, response and re-verification commit/test on
   #1908. No preflight with an open blocking finding.
2. Fix the candidate commit: #1908's merges, merged #1859 and
   any merged follow-up named by the maintainer, including A2. Rebuild the
   trusted binary there; use it throughout.
3. Meet provisioning above; require the old launcher's sweep clean before
   switching. Obtain separate window/two-home-swap approval (3); follow the
   state/restoration rule above for browser probes, mask preflight, stage A
   smoke and #1700 stage B's [write-back probes](argon-verify-writeback.md).
   Browser results cannot replace stage B: each needs its own pass, record
   and enabling word, on the same candidate in the same window.
4. Record the browser stage on #1700 in its required format: every
   deterministic pass/fail and unmet item; retain extended preflight's
   commands, logs and restoration evidence above. Stage B keeps a
   separate record. Past results are not approval.
5. Obtain separate launcher-update approval (4). Switch both workers in one
   window and all container-starting callers to the new binary; include
   smoke/rollback checks. Run the new launcher's sweep before rollback.
   `test-browser` smoke uses the paused row above, not unknown-check refusal.
6. After launcher update, obtain and record stage B's enabling word (5),
   then separate production `browser-mode on` approval (6), in that window
   or later. Approval 3/results supply neither. List every unmet
   item. Only later documentation pull requests state the stages are in
   force. Eligibility widening needs its own process issue, after preflight,
   launcher update, `browser-mode on` and smoke.
