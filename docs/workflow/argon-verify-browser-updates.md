# Verify browser image updates

**Merged but not in force:** the Chromium image is [built and recorded][build];
A2 pins `containerVerifyImage` to that ID in source. Not in force: the enabled
launcher `argonreview-2e3a6bf` (both workers) still pins the old image (#1697)
and is unaffected. Only a launcher built from a commit including A2 uses the
new image, after the integrated preflight and the maintainer's separate
approval. Stage B and `browser-mode on` each need their own.
Nothing here enables or authorizes anything;
[preflight and approvals](argon-verify-browser-preflight.md) remain separate.
PR A changed no runner, flags or launcher. B3a's masking is merged;
A2 had to merge after B3a, with its masking tests passing.

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
against the currently pinned image's record. The first browser build used
the old image's #1697 record, not a new
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
- The image size against the currently pinned image's size;
  state both sizes and the difference.
- The build time.
- One headless `about:blank` run under the production flags, with its
  result. Here, "production flags" means the container flags of the browser
  check as pinned by the argument golden; this PR does not define new ones.

The [first browser build][build] recorded **2,829,756,460 bytes** (+60.3 %).
These requirements apply to every approved build. The record, not PR A's
recipe alone, establishes the image ID. A2 pins it after record review and
the B3a prerequisite below.

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
3. **Merged B3a masking, A2 pin, then preflight and launcher:**
   without masking, `test` and `test-db` would find `/usr/bin/chromium`
   through go-rod's `launcher.LookPath` and
   launch Chromium outside `browser-mode`, `browser.lock`, the 2-unit
   budget and the 14-day gate. `test-db ./cmd/ribbitto` could even return
   `verified` after a browser run. Chromium must run only through
   `test-browser`.

   First, B3a (#1908 runner wiring) merged after M3, B1 and B2.
   It runs every non-browser check with Chromium unreachable:
   `--tmpfs /usr/lib/chromium:ro,noexec,nosuid,nodev,size=1m` masks Debian's
   install directory. Only `test-browser` omits it.
   `TestContainerChromiumMasked` and `TestContainerArgumentsGolden` pin the
   masking and prove a non-browser check cannot launch Chromium from its
   final arguments. These image-free tests let B3a precede A2; both had to
   pass before B3a merged.

   Second, A2 pins `containerVerifyImage` to the recorded ID, with tier A
   review and merge on the maintainer's instruction. A2 must not merge until
   B3a is merged and its masking tests pass; it does not wait for the
   preflight. If masking would weaken another boundary, stop and report on
   #1908 before A2. PR A changed no runner.

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

Later Debian Chromium fixes repeat this path and package diff review.
[Costs](argon-verify-browser-record.md#update-costs).

## Weekly check and expiry

Tracking issue **#1910** holds the weekly records. Claude checks by hand at the
first session each **Monday (JST)**; if none, at the next session, stating
the delay. Handle advisories known between checks at once.

1. Take installed versions from the production image's host-build record
   (`dpkg-query -W` list and image ID), and recipe pins from `origin/main`.
2. Read the Debian security tracker `chromium` page, current trixie-security
   version and DSAs since the last record; note each DSA for an installed package.
3. Read Chrome stable-channel release notes since the last record: CVE
   severities and whether in-the-wild exploitation is stated.
4. Classify against the immediate-pause triggers below: follow the emergency
   procedure if any applies; start the update path the same day for another
   applicable fix; otherwise record decision none.
5. Compare the `dpkg-query -W` diff of any build since the last record, or
   write "no build since <date>".
6. Post the record on #1910 in its record format.

Steps 1–3 and 5 are read-only web/file reads. The check reads public
advisories and recorded package lists: no builds, pulls, pin changes or
worker-home access. Updates need separate approvals.

Use `Result: complete at <time>` only when **every record line** is filled
from a successful fetch and the decision step was carried out. Any missing
item or unreadable/unfetchable source means `Result: incomplete`: it never
counts and does not move the deadline. State what to retry.

Before `browser-mode` is first set to `on`, post the first `Result: complete`
record on #1910 and write its time with `browser-check-record`; `on` is not
set without both (#1910 Done-when and Owner/mechanism; #1908 Preconditions).

After each complete record, Claude runs host-only
[`browser-check-record`](argon-verify-browser-record.md#initial-and-routine-recording)
with its time and URL: first via `--init` and the frozen wrapper, later
without a switch path. Never record an incomplete check's time.

At **14 days or more** since the last complete record, or if none exists,
Claude writes `off`; re-enabling needs the maintainer. Check `browser-mode`
at every session start and before any Argon run or `container-check`.
The runner's [gate](argon-verify-browser-record.md#gate-file) enforces expiry
at each browser call without a session: exactly 14 days expires; missing,
malformed, unsafe or future entries fail closed. Expired refresh while `on`
is refused. Pause, refresh and re-enable stay separate. Scheduled monitoring
to flip the switch is a follow-up needing approval.

## Security fixes and emergency pause

Exploitation in the wild, Critical severity, container escape, or an
exploitable High or above with serious impact without the sandbox triggers
the immediate [emergency pause](argon-verify-browser.md#removal-authority-and-emergency-pause).
Other fixes start the update path immediately; records and fixed images
authorize no re-enabling.

[build]: https://github.com/tkakkie/ribbitto/issues/1908#issuecomment-6462194722
