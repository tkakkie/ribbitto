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
   rule and what is verified), iOS Safari included.
3. Accessibility semantics (roles, states, focus, reading order;
   [`accessibility.md`](../accessibility.md)) are unchanged.
4. It is not a trick that the next reader, human or AI, has to decode.

A script doing what HTML or CSS does under the criterion is a review finding,
and so is HTML or CSS that fails one of the four points.

## HTML and CSS in today's code

- **Popover API menus (`popover`, `popovertarget`):** `#account-menu` and
  `#channel-create` in `internal/web/view/shell.templ`, and `messageActions` in
  `internal/web/view/message.templ` (`anchor-below-end` in `web/styles/app.css`).
  The browser opens, dismisses on `Escape` or outside tap, and returns focus
  without JavaScript.
- **`:has()` in `internal/web/view/branch.templ`:** `#branch-form` uses
  `peer-not-has-checked/list:hidden` and `peer-has-checked/list:absolute` (or
  `short:peer-has-checked/list:static`) against `#message-list` (`peer/list` in
  `internal/web/view/channel.templ`). It appears from the first paint only while
  a message is checked; `display: none` keeps its controls out of the tab order
  and accessibility tree, and covering the composer avoids resizing the list.
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

## Scripts that stay

- **`web/static/message-time-v2.js` (`Intl`):** formats `<time data-local-time>`
  in the reader's locale and time zone with `Intl.DateTimeFormat` on load and
  scoped `htmx:load`, while the server renders UTC text.
- **`web/static/side-panel-v3.js` (`inert`, `aria-modal`, a focus trap):** below
  `wide`, `#side-panel` (`internal/web/view/panel.templ`) covers the
  conversation ([`ui-components.md`](../ui-components.md#side-panels)). CSS
  places the overlay or sheet, but only the script sets `inert` on background
  siblings, sets `role="dialog"` and `aria-modal="true"`, moves and traps focus,
  and remembers the opener in `sessionStorage`.
- **`web/static/popover-focus-v1.js` (closing on `Tab`):** popovers are
  non-modal, so `Tab` or `Shift+Tab` can leave `:popover-open` open over the
  newly focused control; the script hides it once `Tab` moves focus outside.
- **`web/static/branch-selection-v1.js` (`disabled`):** checking a message
  disables checkboxes from other source topics (`data-source`), the source
  topic's `<option>` in `#branch-to`, and `#branch-name` when an existing topic
  is selected, including on `htmx:load`. CSS cannot set `disabled`.
- **`web/static/arrival-v1.js` (navigation type):** reads
  `navigation.activation` before first paint to set `data-arrive-pill` and
  `data-arrive-panel` on `document.documentElement` only on a same-origin `push`
  navigation that changed the place or opened a panel, clearing the marks on
  animation end, `#sidebar-channels` swap or `DOMContentLoaded`.
- **Composer `visualViewport` handling in `web/static/message-composer-v21.js`:**
  iOS Safari's keyboard shrinks and pans the visual viewport without changing
  `dvh` (point 2). The script writes `--composer-viewport-h` and
  `--composer-viewport-top` on `#organization-stream` when `visualViewport`
  differs from `innerHeight` or `0` by more than 1 px, with `100dvh` as the CSS
  fallback ([`ui-composer.md`](../ui-composer.md)).
- **Scroll position (`web/static/message-history-v2.js`,
  `web/static/sidebar-scroll-v2.js`, and list anchoring in
  `web/static/message-composer-v21.js`):** replacing message-list anchoring with
  `flex-direction: column-reverse` or `overflow-anchor` fails points 3 and 4
  today, and centring the active sidebar row without moving the window or focus
  requires `scrollTop`.
