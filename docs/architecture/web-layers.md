# Web layers: handlers, templ, htmx and JavaScript

[Decision 19](../decisions/19-web-layers-server-owned-html-htmx-swaps-javascript-as-enhancement.md).
[Browser targets](browser-targets.md).

## Progressive enhancement

Core functionality works without JavaScript; progressive enhancement need not
provide full parity.

- **Core, today:** first-run setup, sign-up, sign-in and sign-out, opening
  a channel or topic with its history (including older pages) and its members or overview panel, posting a message,
  branching, resolving and reopening a topic, pinning and unpinning a message, marking a feed or topic read, and creating channels or changing their images. Plain forms and links work alone;
  htmx and scripts improve them.
- **Enhancement only:** real-time updates, focus and scroll handling (while CSS starts a latest conversation page at its newest entry without JavaScript),
  keyboard shortcuts (chosen send key), local-time display, own-mention outlines,
  pending own posts on feeds/topics and drafts on the device ([decision 37](../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md)). Without
  JavaScript a page may lack these, but nothing core breaks.

## Go handlers

Handlers in `internal/web` validate requests, call use cases, convert results
to view models, and choose status, component and full page or fragment. They never
build HTML strings (`AGENTS.md`); `/healthz` and `/readyz` return plain text.
Unit tests fake web seams, never conversation's storage ports.

## templ

Components render view models in `internal/web/view`. Handlers convert
module results; components never receive them. Views import neither `org` nor
`conversation`; from `kernel`, only `ID`. Components do no I/O and
make no authorisation decisions; display logic (building URLs,
formatting, choosing an i18n message ID) belongs in them.

## htmx

See [`web-htmx.md`](web-htmx.md#htmx).

## JavaScript

See [JavaScript](web-javascript.md), [HTML/CSS first](web-css-first.md) and
[conventions](web-conventions.md) ([decision 43](../decisions/43-typescript-with-a-go-driven-build.md)
planned amendment).

**Current exceptions** to the no-application-state rule:
[decision 34](../decisions/34-live-unread-counts.md) lets one script own sidebar
badge numbers and bounded recovery state without creating elements or
requests; the server still renders the badges and htmx sends resyncs.
[Decision 37](../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md)
allows bounded composer state (pending posts, idempotency keys, session-bound
drafts); implemented uses follow the [composer contract](web-composer.md).
Requests stay with htmx. [Composer growth](../ui-composer.md) uses CSS;
the composer script keeps the reader's position as the list resizes.

## How the rules are checked

[Client test layers](web-tests.md) and [toolchain checks](web-toolchain-checks.md).
