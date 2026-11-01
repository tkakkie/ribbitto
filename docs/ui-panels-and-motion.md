# UI panels, short heights and arrival motion

Side panels, short-viewport behaviour and arrival motion for the shared
[UI components](ui-components.md).

## Side panels

`sidePanel` renders one `<aside id="side-panel">` for every width, and the
breakpoints ([`ui-code.md`](ui-code.md#dimension-tokens)) place it (#972):

- **From `wide` (1200 px)** a block beside the conversation, `w-panel` wide.
- **From `desk` to `wide`** an overlay: absolute over the conversation's
  right side, with `shadow-popover`, in the conversation's region (`relative`).
- **Below `desk`** a bottom sheet: fixed to the bottom edge, full width,
  rounded only at the top, above the bottom safe area. Its height comes from
  `sheetHeight(panel)`: members and the overview at most 80 %
  (`max-desk:max-h-4/5`). The sheet scrolls itself, with `overscroll-contain`.

Below `wide` the overlay and the sheet are one modal: `#panel-backdrop` dims
the whole page in `scrim`, a link to the close URL that only a pointer
reaches. The panel covers the conversation exactly while it is out of flow.
`web/src/side-panel.ts` alone decides that, from the panel's computed `position`
on load and on each resize, and makes it a modal dialog over an inert page
([accessibility](accessibility.md)); its `aria-modal="true"` is the
published state. `read-visibility-v6.js` reads that attribute and pauses
automatic reading, while the stream still updates the page behind. Each time
it reads, it first dispatches a synchronous `panel-cover` event on the panel,
and the panel script publishes the state for the current size: a page shown
again after a size change while hidden gets its resize event only later, and
no listener order matters. The panel script listens from its deferred run,
before the reader's first read at `htmx:load`. The header's **Mark
as read** is hidden below `wide`, where the overview shows a
`submitButtonFor` the same `#read-form`. Without JavaScript the panel is its
own page: its links still close it and page the members.

## Short heights

Below `short:` (436 px, #1056), the [composer](ui-composer.md) rests at
38 px from `desk`, 44 px below it, and grows up to three lines (82 px).
The list keeps 128 px (`h-32`), a few short messages.
Here `#conversation` scrolls as a whole (`short:overflow-y-auto`)
and the list is fixed at `h-32` (`short:flex-none`). It is still the pane the
composer, history and stream scripts move, so they need no change. The
frame uses `h-conversation-viewport` (`dvh` unless the visual viewport differs).
A minimum height would let the sidebar and adjacent panel grow to their
content and stop scrolling themselves.

Two things differ there. A covering move form would scroll away from the
composer with the conversation, so the form follows the list in the column
and the composer is hidden; the fixed list still does not resize. A post
scrolls its message into view (`message-stream-v14.js`), which there also
scrolls the conversation up from the composer.

## Arrival motion

The selection pill and side panel move only on arrival
([motion](ui-motion.md#motion), #921). `web/src/arrival.ts` marks the root
(`data-arrive-pill`, `data-arrive-panel`) and CSS animates under those
marks. It reads `navigation.activation` and stores nothing: only a `push`
whose previous entry is same-origin (a link, or a form's redirect) is
marked, the pill when the place (the path without `/members` or
`/overview`) changed, the panel when one newly opened. A first load, a
reload or Back/Forward is never marked; neither is a panel switch or close.
A mark is removed when its animation ends, the pill's earlier at the first
live `#sidebar-channels` replacement, so a live list never grows its pill
again; one with nothing to animate (reduced motion, a hidden row) is
removed at `DOMContentLoaded`. The script sits in `<head>` and is not
deferred: it must mark the page before the first paint, or the pill shows
at rest first. Browsers without the Navigation API show no arrival motion.
`testArrivalBrowser` (`internal/web/arrival_browser_test.go`) checks these
cases.

Below `desk` a conversation hides the sidebar, so its pill never moves; the
list page's pill grows in when a conversation's back link leads there, and
an opening panel rises as the sheet, because `rb-panel-in` is redefined
under `max-desk:`. `testMotionTouchBrowser`
(`internal/web/motion_touch_browser_test.go`) checks that nothing else moves.
