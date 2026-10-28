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
   use the [dev/exclusive procedures](argon-verify-budget-start.md#start-procedure) and
   [dev and load containers](argon-verify-budget-dev.md).
3. Run the old launcher's `container-sweep` first; require no leftover before
   switching. After A2, the new sweep cannot reclaim old-image
   leftovers; they fail every check closed. Never switch around them.
4. Use one new build for all callers: both workers' run/check wrappers and
   Claude's `container-check` and `container-generate`. Older callers take
   no slots; never mix launchers.
   Both workers' run/check wrappers pass the same absolute worker-1
   [browser switch path](argon-verify-browser.md#switch-and-admission-order)
   only with `--verify on`; worker 2 reads worker 1's file, never a separate
   copy. With `--verify off` (`checks.zsh` idle check or `verify-mode off`
   rollback), never pass `--browser-mode`: the commands refuse it, so an
   unconditional flag would make them refuse. `container-check` accepts it
   only for `test-browser` and refuses it for other checks; absent means paused.
5. Complete the first [weekly record](argon-verify-browser-updates.md#weekly-check-and-expiry)
   and run `browser-check-record --init` via the frozen
   wrapper with every [initialization check](argon-verify-browser-record.md#initial-and-routine-recording):
   regular uid-owned switch, uid-owned 0500 `implementer/`, regular
   `verify-mode` beside it. `--init` also refuses a switch that reads as
   enabled; it requires neither exact `off` content nor 0400. Claude or the
   host-side frozen wrapper with pinned binary hash must still confirm
   `off`/0400 before `--init` as operator rules.
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

Follow the [numbered browser probes](argon-verify-browser-probes.md#numbered-browser-probes),
including the window's state and restoration rules.
For cap/kill observations, attribution to our own cancellation is
best-effort; see the [accepted residual risk](argon-verify-testdb.md#result-summary).

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
   [state/restoration rule](argon-verify-browser-probes.md#numbered-browser-probes)
   for browser probes, mask preflight, stage A
   smoke and #1700 stage B's [write-back probes](argon-verify-writeback.md).
   Browser results cannot replace stage B: each needs its own pass, record
   and enabling word, on the same candidate in the same window.
4. Record the browser stage on #1700 in its required format: every
   deterministic pass/fail and unmet item; retain extended preflight's
   commands, logs and [restoration evidence](argon-verify-browser-probes.md#numbered-browser-probes).
   Stage B keeps a separate record. Past results are not approval.
5. Obtain separate launcher-update approval (4). Switch both workers in one
   window and all container-starting callers to the new binary; include
   smoke/rollback checks. Run the new launcher's sweep before rollback.
   `test-browser` smoke uses the
   [paused row (probe 7)](argon-verify-browser-probes.md#numbered-browser-probes),
   not unknown-check refusal.
6. After launcher update, obtain and record stage B's enabling word (5),
   then separate production `browser-mode on` approval (6), in that window
   or later. Approval 3/results supply neither. List every unmet
   item. Only later documentation pull requests state the stages are in
   force. Eligibility widening needs its own process issue, after preflight,
   launcher update, `browser-mode on` and smoke.
