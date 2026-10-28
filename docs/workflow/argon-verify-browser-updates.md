# Verify browser image updates

**Merged but not in force:** no image with Chromium has been built or
pinned, the launcher is unchanged, and nothing here enables the browser
check (#1908). Building, pinning, the launcher update and `browser-mode on`
each need the maintainer's separate approval. This page records PR A's
recipe and update contract; it does not authorize those later steps.
Nothing here enables or authorizes anything; the
[preflight and approvals](argon-verify-browser-preflight.md) remain separate.
PR A changes no runner, flags or launcher. B3a implements the masking below
as a precondition for A2; A2 must not merge before B3a is merged.

## Recipe pins

The pins in `cmd/argonreview/verifyimage/Dockerfile` are:

| Package | Exact recipe argument |
|---|---|
| Debian Chromium | `chromium=156.0.8078.47-1~deb13u1` |
| Liberation fonts | `fonts-liberation=1:2.1.5-3` |

These versions were verified by the orchestrator on 2026-10-28 against the
Debian security tracker and the trixie-security arm64 Packages index
(Chromium), and trixie (fonts). The base is Debian 13, trixie, arm64.
`containerChromiumVersion` in `cmd/argonreview/container.go` must equal
Chromium's recipe version. `TestVerifyImageRecipe` checks that equality,
that each recipe pin appears exactly once, and that this page contains both
pins. Change the recipe, constant and this page together.

Pins go stale: trixie-security keeps only the newest Chromium build, so a
pin may become unfetchable within weeks. Just before requesting build
approval, re-check Debian with `apt-cache madison` for the architecture or
the trixie-security arm64 Packages index. If the pin is gone, start a new
pin pull request; never use snapshot.debian.org or another source without
an amendment.

The recipe test refuses browser installs after PGDG setup, whether later
in the same RUN or in a subsequent RUN; earlier installs in that RUN are
allowed. Chromium and fonts initially install from Debian, but a later
`postgresql-18` install can upgrade shared libraries from PGDG; the
`dpkg-query -W` diff review covers those changes. Transitive packages float, like
`postgresql-client-18` and `libpq5` today; exact top-level pins do not imply
identical build bytes. A pin that cannot be fetched makes the build fail.
It never falls back to the latest version; use the update path below.

`TestVerifyImageRecipe` refuses `upgrade`, `dist-upgrade`, `full-upgrade`,
`--allow-downgrades`, wildcards, an unpinned package or any other package,
and browser installs after PGDG setup in the same or a later RUN.
Installs need `-y` and `--no-install-recommends`; only the recipe's allowed
packages may be named. This is a check, not a security boundary. As in
[decision 42](../decisions/42-repository-languages-and-formats.md#language-list),
the boundary is the container flags and the pinned image ID in trusted code.
See the image page's [recipe rules](argon-verify-image.md#recipe-rules).

## The `dpkg-query -W` diff rule

Every build records the full `dpkg-query -W` list. The reviewer diffs it
against the list recorded for the image currently pinned. For the first
browser build, compare against the current image's #1697 record, not a new
build of the old recipe: floating dependencies could already have changed.
List every added, removed or changed package, including old and new
versions where applicable; an empty diff must also be recorded explicitly.

Unexpected or security-relevant changes need review and justification
before the image ID is pinned. This includes the C library, TLS and crypto
libraries, graphics and font libraries, anything Chromium loads, and
packages whose purpose is not obvious. Explain why each such change is
needed and acceptable for this image. An unexplained change blocks the
pin, even if the build and headless run succeed. The reviewed package diff
is part of the build record, not a replacement for reviewing the recipe.

## Build record items

Follow the image page's [host build procedure](argon-verify-image.md#host-build-procedure)
and retain everything it already requires: the full approved commit,
SHA-256 of all three context files, build log tail, image ID and size, Go,
templ and sqlc versions, the Tailwind banner, PostgreSQL version and package
list (confirming `postgresql-common` remains 278), and SHA-256 of the three
installed generator binaries. Record the browser build on #1908 with:

- `chromium --version` from the built image.
- The full `dpkg-query -W` list and its reviewed diff against the current
  image, following the rule above.
- The image size against the current image's **1,765,140,168 bytes**;
  state both sizes and the difference.
- The build time.
- One headless `about:blank` run under the production flags, with its
  result. Here, "production flags" means the container flags of the browser
  check as pinned by the argument golden; this PR does not define new ones.

These are requirements for a later approved host build, not results from
PR A. The record establishes what was actually built; the recipe alone
cannot establish an image ID. PR A2 uses the recorded ID only after review
and the B3a prerequisite below.

## Update path

There is no light Chromium-only update path. Two pull requests and a host
build are required, in this order:

1. **Pin pull request:** update recipe pins and `containerChromiumVersion`
   together, including this page. Obtain tier A review and merge on the
   maintainer's instruction.
2. **Host build:** build from the merged commit only, following the image
   page's [host build procedure](argon-verify-image.md#host-build-procedure).
   List the downloads, then obtain the maintainer's chat approval before
   building. Record the image ID and all build record items above. There
   is no exception for building an unmerged recipe on the host.
3. **B3a masking, then image-ID pull request (A2), preflight and launcher:**
   once the pinned image contains `/usr/bin/chromium`, existing `test` and
   `test-db` checks would find it through go-rod's `launcher.LookPath` and
   launch Chromium outside `browser-mode`, `browser.lock`, the 2-unit
   budget and the 14-day gate. `test-db ./cmd/ribbitto` could even return
   `verified` after a browser run. Chromium must run only through
   `test-browser`.

   First, B3a (#1908 runner wiring) merges, once M3, B1 and B2 are
   satisfied. B3a runs every non-browser check with Chromium unreachable:
   `--tmpfs /usr/lib/chromium:ro,noexec,nosuid,nodev,size=1m` masks Debian's
   install directory. Only `test-browser` omits it.
   `TestContainerChromiumMasked` and `TestContainerArgumentsGolden` pin the
   masking and prove a non-browser check cannot launch Chromium from its
   final arguments. These tests need no image, so B3a does not wait for A2,
   and both must pass before B3a merges.

   Second, A2 pins `containerVerifyImage` to the recorded ID, with tier A
   review and merge on the maintainer's instruction. A2 must not merge until
   B3a is merged and its masking tests pass; it does not wait for the
   preflight. If masking would weaken another boundary, stop and report on
   #1908 before A2. This is a precondition recorded here; PR A implements
   no runner change.

   Third, build a candidate binary from a pinned commit that includes A2.
   Its integrated preflight must verify `dpkg -L chromium` places the binary
   under the masked directory and that no non-browser check can run Chromium
   on the real image. Follow the full
   [mask preflight](argon-verify-browser-preflight.md#chromium-mask-preflight),
   including an ordinary masked check and verification that the separate
   tmpfs mount is harmless with `--read-only`. The launcher must not be updated
   until that preflight succeeds; then update it on the maintainer's
   separate approval. The preflight and the launcher update each need that
   approval.

A later Debian security fix to Chromium repeats this path, including the
package diff review. Every update costs a tier A pin pull request, an approved
host build, a tier A image-ID pull request, a preflight subset and a separately
approved launcher update; this can recur about weekly. The issue's image
estimate is roughly +1 GB (2.82 GB versus 1.77 GB); the build record must
establish the actual size.

## Weekly check and expiry

Claude checks by hand **every Monday (JST)** and records it on the weekly
tracking issue. Use that issue's maintainer-defined items and sources as
the record; this page adds no substitute item list. Fetch every source and
finish every item. A record counts only with **`Result: complete`** and
every item done and every source fetched. An incomplete record does not
reset the clock.

After each complete record, Claude runs the host-only
[`browser-check-record`](argon-verify-browser-record.md#initial-and-routine-recording)
with its completion time and record URL. The first entry uses `--init`
through the frozen wrapper; routine updates take no switch path. Do not
record a completion time for a check that is incomplete.

If **14 days or more** have passed since the last complete record, or none
exists, Claude writes `off` to `browser-mode`. Re-enabling always needs the
maintainer. At every session start and before any Argon run or
`container-check`, Claude checks the switch itself against this rule.
The runner's [gate](argon-verify-browser-record.md#gate-file) enforces expiry
mechanically at each browser call without waiting for a session: an entry
exactly 14 days old is expired. Missing, malformed, unsafe or future entries
also fail closed. Refreshing an expired entry while its switch reads `on`
is refused; pausing, refreshing and re-enabling are distinct steps.
Scheduled monitoring that flips the switch remains a follow-up and needs
its own approval.

## Security fixes and emergency pause

Exploitation in the wild, Critical severity, container escape, or an
exploitable High or above with serious impact without the sandbox triggers
Claude's immediate [six-step emergency pause](argon-verify-browser.md#removal-authority-and-emergency-pause):
switch off, list by browser label, apply full-ID emergency removal checks,
sweep, confirm no browser container or copy and every slot free, record and
tell the maintainer. Other fixes start the update path immediately.
Neither a complete weekly record nor a fixed image supplies re-enabling
approval.
