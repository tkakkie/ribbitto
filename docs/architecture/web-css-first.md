# HTML and CSS before JavaScript

When to use HTML and CSS platform features instead of a script
([decision 19](../decisions/19-web-layers-server-owned-html-htmx-swaps-javascript-as-enhancement.md);
[`web-layers.md`](web-layers.md), [`web-javascript.md`](web-javascript.md),
[`web-conventions.md`](web-conventions.md)).

What HTML and CSS gain: they work from the first paint and without JavaScript;
they run off the main thread where the browser can, so they do not compete with
input (INP); they have no ordering races with htmx swaps and SSE insertions; and
code that does not exist does not break.

What forced CSS costs: tricks that separate visual order from DOM order
(screen-reader and keyboard order, scrolling to the unread divider); features
missing in a target browser, which then need a script fallback anyway; semantics
CSS cannot express (`inert`, `aria-modal`, `disabled`, moving focus); and
behaviour that only a browser test can check.

## Criterion

Use HTML or CSS instead of a script only when **all** of these hold; otherwise
write the script ([`web-conventions.md`](web-conventions.md)):

1. It covers the behaviour completely, with no script fallback.
2. It works in the [target browsers](browser-targets.md) (the list, the version
   rule and what is verified; where a CSS feature is verified only in Chromium,
   its entry below states that Firefox and Safari are expected, unverified),
   iOS Safari included.
3. Accessibility semantics (roles, states, focus, reading order;
   [`accessibility.md`](../accessibility.md)) are unchanged.
4. It is not a trick that the next reader, human or AI, has to decode.

A script doing what HTML or CSS does under the criterion is a review finding,
and so is HTML or CSS that fails one of the four points.

## HTML and CSS in today's code

- **Popover API menus (`popover`, `popovertarget`):** `#account-menu` and
  `#channel-create` (when `popover` is true, i.e. `Error == ""` and
  `CreateImageError == ""`; in place on error) in
  `internal/web/view/shell.templ`, and `MessageActions` in
  `internal/web/view/message.templ` (`anchor-below-end` in `web/styles/app.css`).
  The browser opens, dismisses and restores focus. Message controls need
  JavaScript for the shared popover; otherwise they link to the actions page.
- **`:has()` in `internal/web/view/branch.templ`:** without errors, show the
  branch form from first paint only while a message is checked; keep hidden
  controls out of the tab order and accessibility tree. Errors stay in the
  column. Covering the composer avoids resizing the list; short screens keep
  the form in flow.
- **`field-sizing-content` for composer growth:** `#message-body` in
  `internal/web/view/channel.templ` pairs `field-sizing-content` with
  `max-h-composer` in `web/styles/app.css` ([`ui-composer.md`](../ui-composer.md)).
  Text grows the textarea up to the cap without a sizing script; browsers without
  `field-sizing` keep the `rows="3"` box with inner scrolling.
- **CSS motion with `motion-safe`:** rows and buttons in
  `internal/web/view/channel.templ`, `internal/web/view/shell.templ` and
  `internal/web/view/message.templ` use
  `transition-feedback motion-safe:active:scale-96`; `web/styles/app.css` gates
  arrival keyframes on `prefers-reduced-motion: no-preference` and clears motion
  under `reduce` ([`ui-motion.md`](../ui-motion.md#motion)).
- **The URL fragment that scrolls the sidebar:** below `desk`, the conversation
  back link in `internal/web/view/channel.templ` targets `#channel-<id>`
  (`ChannelDOMID`) on the channel list page, scrolling that row into view without
  JavaScript ([`ui-code.md`](../ui-code.md#sidebar-order-and-position)).
- **Reversed flex container for the no-JavaScript latest page:** only with
  `scripting: none`, a single wrapper child keeps DOM, reading and Tab order
  unchanged while the scroller starts at the newest entry from first paint;
  short and empty pages remain top-aligned or centred. Under *Criterion* point 2,
  `TestBrowser/message-list/no-javascript` verifies Chromium locally and in CI;
  Firefox (113+, reversed scroll origin since 81) and Safari / iOS Safari (17.0+)
  are **expected, unverified**. Unsupported browsers open the pane at its top.
  With JavaScript the wrapper is `display: contents`.

## Scripts that stay

- **`web/src/message-time.ts` (`Intl`):** formats `<time data-local-time>`
  in the page's `lang` (`document.documentElement.lang`) and the reader's time
  zone with `Intl.DateTimeFormat` on load and scoped `htmx:load`, while the
  server renders UTC text (point 1: CSS cannot do `Intl` formatting).
- **`web/src/side-panel.ts` (`inert`, `aria-modal`, a focus trap):** below
  `wide`, `#side-panel` (`internal/web/view/panel.templ`) covers the
  conversation ([`ui-panels-and-motion.md`](../ui-panels-and-motion.md#side-panels)). CSS
  places the overlay or sheet, but only the script sets `inert` on background
  siblings, sets `role="dialog"` and `aria-modal="true"`, moves and traps focus,
  and remembers the opener in `sessionStorage`.
- **`web/src/popover-focus.ts` (closing on `Tab`):** popovers are
  non-modal, so `Tab` or `Shift+Tab` can leave `:popover-open` open over the
  newly focused control; the script hides it once `Tab` moves focus outside.
- **`web/src/branch-selection.ts` (`disabled`):** checking a message
  disables checkboxes from other source topics (`data-source`), the source
  topic's `<option>` in `#branch-to`, and `#branch-name` when an existing topic
  is selected, including on `htmx:load` (points 1 and 3: CSS cannot set
  `disabled`).
- **`web/src/arrival.ts` (navigation type):** CSS cannot read
  `navigation.activation` (point 1). Before first paint, mark only same-origin
  `push` navigation that changed the place or opened a panel;
  marks must not survive ended, cancelled or inactive animations; clear the pill
  mark on sidebar swaps.
- **Composer `visualViewport` handling in `web/src/composer.ts`:**
  iOS Safari's keyboard shrinks and pans the visual viewport without changing
  `dvh` (point 2). The script writes `--composer-viewport-h` and
  `--composer-viewport-top` on `#organization-stream` when `visualViewport`
  differs from `innerHeight` or `0` by more than 1 px, with `100dvh` as the CSS
  fallback ([`ui-composer.md`](../ui-composer.md)).
- **Scroll position (`web/src/message-history.ts`,
  `web/src/sidebar-scroll.ts`, and list anchoring in
  `web/src/composer.ts`):** these with-JavaScript anchoring scripts stay.
  Replacing with-JavaScript message-list anchoring with
  `flex-direction: column-reverse` (#1480) or `overflow-anchor` fails point 4
  today (whereas the `(scripting: none)` single-child wrapper on the latest
  page, #1489, keeps reading order and never runs alongside the scripts), so
  with-JavaScript anchoring is left to the prototype and measurements of
  `#1361`; centring an out-of-view `[aria-current='page']` sidebar row on load
  without moving the window or focus fails point 1.
