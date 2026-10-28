# Browser preflight and enabling

**Merged but not in force:** the internal browser runner is merged;
nothing here enables or authorizes anything. For the candidate, see
[operation](argon-verify-browser.md),
[security records](argon-verify-browser-record.md) and
[image updates](argon-verify-browser-updates.md).

## Separate approvals

After `ready`, approvals stay separate and fresh, in order: each merge on
the maintainer's instruction; the host image build in chat after listing downloads; the
preflight window and its temporary two-home swap; the production launcher
update (date, commit and procedure fresh; past results are not approval);
stage B enabling; `browser-mode on`; and any scheduled task (follow-up).
Later Chromium updates repeat the host-build and launcher-update approvals
through the [update path](argon-verify-browser-updates.md#update-path).

## Provisioning before enabling

Claude prepares and records these preconditions under the host approvals:

1. Follow [host lock provisioning](argon-verify-budget.md#host-lock-provisioning)
   exactly: `/var/cache/argonreview/budget/` and its parent
   `/var/cache/argonreview/` are real directories owned by the uid running
   every caller, not group- or world-writable (e.g. 0700; a uid-owned
   0755 parent is acceptable). The runner never creates them. Every
   sandbox must permit creating and locking files there. Slot locks and
   `browser.lock` self-create at 0600 under the documented file checks.
2. Record a `docker ps -a` baseline with no unregistered container. Remove
   the old unregistered `ribbitto-codex-pg`; keep the
   [recorded-ID database lifecycle](argon-verify-budget-start.md#codex-database-lifecycle)
   for later use. Start every later host container through `budget-start`;
   use the [dev/exclusive procedures](argon-verify-budget-start.md#start-procedure).
3. Before switching, run the **old launcher's** `container-sweep` and
   require no leftover. After A2, an old-image leftover cannot be reclaimed
   by the new sweep and fails every check closed. Do not switch around it.
4. Prepare all callers for the same new build: both workers' run/check
   wrappers and Claude's `container-check` and `container-generate`.
   Older callers take no slots; never mix launchers.
   Both workers and `container-check` pass worker 1's single
   [browser switch path](argon-verify-browser.md#switch-and-admission-order).
5. Complete the first [weekly record](argon-verify-browser-updates.md#weekly-check-and-expiry)
   and make its `browser-check-record --init` entry through the frozen
   wrapper, following every [initialization check](argon-verify-browser-record.md#initial-and-routine-recording):
   regular uid-owned switch, uid-owned 0500 `implementer/`, regular
   `verify-mode` beside it; switch 0400. The gate directory also holds
   `browser-security-check.lock` for record writers.

## Chromium mask preflight

A2 must not merge before B3a is merged and `TestContainerArgumentsGolden`
and `TestContainerChromiumMasked` pass. Those tests inspect arguments
without an image; real-image preflight remains required. If masking
would weaken another boundary, stop and report on #1908 **before A2**.

On the real image, verify with `dpkg -L chromium` that the executable files
needed to launch Chromium sit under `/usr/lib/chromium`, the directory masked
for every non-browser check by
`--tmpfs /usr/lib/chromium:ro,noexec,nosuid,nodev,size=1m`.
Only `test-browser` omits it. This separate tmpfs mount neither writes to
nor makes the read-only root writable.
The argument tests also reject later overlapping mounts or browser
environment/script overrides that would defeat the mask.

Verify that a non-browser check cannot launch Chromium on that image and
run one ordinary check with the mask before the launcher update. The
go-rod helpers skip for a missing or unlaunchable browser without
`RIBBITTO_REQUIRE_BROWSER=1`; non-browser checks do not set it. A wrapper
found on `PATH` alone is not evidence that its masked binary can launch.
Record the skip/launch refusal; unit tests establish neither timing nor
a real browser run. The mask must be harmless
with `--read-only`, including on the image without Chromium.

## Numbered browser probes

The probes run through the public `test-browser` call of the candidate
binary. Use deterministic conditions; timing-dependent contention is never
a pass condition. Argon reads each result inline. Keep stage A's refusal,
smoke, restoration and evidence requirements from
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
   2,048 threads is never `verified`. Afterwards require no container,
   no source copy and no slot left held.
4. **Controlled holders:** use dummy dev containers on the pinned verify
   image, `--network none --read-only`, 1 GiB, one slot each, `sleep 900`,
   started through `budget-start`; use a 300 s browser test to hold
   `browser.lock`. With holders on slots 1 and 2, require a `test` call
   `verified` and `test-browser` busy after **120 s ± 5 s**. With all three
   slots held, a 1-unit call is busy. While the browser holder runs, a
   second `test-browser` is busy. Remove a holder 30 s into a wait: require
   `verified` and a log showing a wait of 30 s or more. With no holders,
   three 1-unit calls are all `verified`. A `SIGKILL`ed runner's leftover
   blocks its slot until `container-sweep`; after that, a call is
   `verified`. A mismatched verification-named container fails closed.
5. **Other containers:** an unlabelled container or a dev container without
   a limit fails closed and is left untouched; after removal the next call
   is `verified` with no approval. `make db-up` fails closed until the wrapper
   runs again. A created exclusive container pauses. Exclusive
   `budget-start` waits out the browser holder; `budget-start` refuses a
   running container, a wrong ID or wrong labels. Run one full Codex database
   lifecycle under its recorded-ID and process checks.
6. **Switch/gate:** `off`, missing or malformed switches from worker 1,
   worker 2 and `container-check` give `NOT VERIFIED: browser check paused`
   with no container started. Expired, future or missing gate entries give
   `NOT VERIFIED: browser check paused: security check overdue`.
   `browser-check-record` refuses expired refresh while the switch is `on`.
   The [emergency pause](argon-verify-browser.md#removal-authority-and-emergency-pause)
   leaves no browser container behind, no copy and every slot free.
7. **Refusals through Argon:** exercise the
   [run-validator rows](../../cmd/argonreview/container_browser_test.go)
   that can be typed, plus stage A's refusal and smoke set. Replace only
   its “`test-browser` is an unknown check” row with guard admission followed
   by `NOT VERIFIED: browser check paused` when `browser-mode` is `off`.
   Keep every other refusal and its absent effect.
8. **Inside the container:** inspect network `none`, read-only root, no
   capabilities, `no-new-privileges`, uid 1000, no seccomp option, pinned
   image and labels. Chrome runs as uid 1000 with `--no-sandbox`. Its loads
   of host listeners on loopback, `host.docker.internal`, 192.168.65.x,
   172.17.0.1 and the LAN address must log no request; a control container
   on the default network must log requests.

## Enabling sequence and record

1. After every prerequisite pull request merges, Claude Opus (primary)
   and one Argon adversarial run review the entire integrated diff. The
   report on #1908 records each finding's severity, response and
   re-verification commit/test. No preflight while a blocking finding is open.
2. Fix the candidate commit: #1908's merges, the merged #1859 change and
   any merged follow-up the maintainer names, including A2. Rebuild the
   trusted binary at that fixed commit; use this one candidate throughout.
3. Meet the provisioning preconditions above and require the old launcher's
   sweep clean before switching. Obtain the separate window/two-home-swap
   approval; run one window with the browser probes, the mask preflight,
   stage A smoke and #1700 stage B's
   [write-back probes](argon-verify-writeback.md).
   Do not substitute browser results for stage B: each stage keeps its own
   pass, record and enabling word, on the same candidate in the same window.
4. Record the browser stage on #1700 in its required format, with each
   deterministic pass/fail and unmet item; retain the commands, logs and
   restoration evidence required by extended preflight. Stage B keeps its
   separate record. Past results are not approval.
5. Obtain the separate launcher-update approval. Switch both workers in one
   window and every container-starting caller to the new binary; include
   smoke and rollback checks. The new launcher's sweep must run before
   reverting for rollback. The smoke row for `test-browser` is the paused
   row above, not unknown-check refusal.
6. Obtain and record stage B's enabling word and, separately, the
   maintainer's approval for `browser-mode on`, in that order, in this
   window or later. List every unmet item. Only later documentation pull
   requests state that the stages are in force. Eligibility widening
   remains its own process issue, after preflight, launcher update,
   `browser-mode on` and smoke.
