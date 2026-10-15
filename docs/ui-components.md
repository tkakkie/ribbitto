# UI components

The shared templ components that pages compose instead of repeating a
control's class string ([decision 33](decisions/33-ui-code-shared-components-and-token-utilities-only.md)).
How templates and styles are written is in [`ui-code.md`](ui-code.md).

**Keep it current:** update this file in the same pull request when a
shared component is added or changes.

## Shared components

A look used in two places becomes a component in `internal/web/view`, and
pages compose components instead of repeating a control's class string.
Today's shared controls:

| Component | File | Use |
|---|---|---|
| `submitButton`, `submitButtonFor` | `internal/web/view/controls.templ` | a form's submit button, labelled by a message ID: `buttonPrimary` (the main action), `buttonSecondary` (outlined; `buttonSecondaryIcon` adds a decorative child, with its text hidden below `desk` and its accessible name kept); `submitButtonFor` submits a form named by its ID from outside it |
| `textLink` | `internal/web/view/controls.templ` | underlined navigation links; callers supply a message ID and extra typography or spacing classes |
| `textInput` | `internal/web/view/controls.templ` | a single-line text input with a control border and a brand underline on focus, alongside the focus outline; the caller supplies its label |
| `textField` | `internal/web/view/text_field.templ` | a labelled `textInput` with its submit error (setup, sign-up); sign-in passes empty field errors and keeps its form-level alert |
| `iconButton` | `internal/web/view/controls.templ` | a button that shows only an icon (its child) and has an accessible name; can open a popover |
| `welcomeLayout` | `internal/web/view/welcome.templ` | the shared brand and content columns for setup, sign-in, sign-up, home and rate-limit pages; below `desk`, a short brand strip without the composition above the content |
| `signOutButton` | `internal/web/view/hello.templ` | the shared POST sign-out form, taking a button kind; home uses `buttonWelcomeSecondary`, and `SignOutButton` keeps `buttonSecondary` for the account menu |
| `unreadBadge` | `internal/web/view/channel.templ` | an unread count with its screen-reader text |
| `Icon` | `internal/web/view/icon.templ` | a decorative Fluent icon from the vendored sprite ([Icons](ui-code.md#icons)) |
| `Avatar` | `internal/web/view/avatar.templ` | a fixed 36 px round initial; six pastel pairs chosen from the member ID, hidden from assistive technology beside the visible name |
| `historyLink` | `internal/web/view/channel.templ` | the rounded older/newest navigation link; only older links carry the history prepend attributes |
| `sidePanel`, `panelControl` | `internal/web/view/panel.templ` | the white block beside the conversation, with a focusable `h2` and a close link, covering the conversation below `wide` ([side panels](#side-panels)); a header link, icon-only below `desk` with its accessible name kept, that opens one panel, `aria-current="true"` and a Filled icon while it is open |
| `imagePicker` | `internal/web/view/channel_image.templ` | a labelled fieldset of native radios with the `brand` accent and focus outline, decorative illustrations and named choices, including the ID-based default; shared by creation and overview |
| `channelImage` | `internal/web/view/channel_image.templ` | a decorative channel illustration from a handler-resolved URL; 24 px and lazy in the sidebar, 32 px in the header, with fixed dimensions and asynchronous decoding |

- **Links navigate, buttons act.** Going somewhere is an `<a href>`;
  anything that changes state is a submit button in a form, even when it
  should look like a link.
- **Components take small inputs** — a label, a kind, a props struct —
  and decide nothing about permissions or data; handlers prepare view
  models (`docs/architecture/web-layers.md`).
- **A new component arrives with its first user.** The account menu came with #872 (`appBar`, using an HTML popover), the avatar with
  #873, the panel with #874, the channel image with #875. When a page is
  restyled, its repeated looks (links, for example) become components too.

Welcome forms select `buttonWelcomePrimary` or `buttonWelcomeSecondary`
for 48 px phone targets; `textField` sets `inputProps.Welcome` for the
same height and `text-entry` (16 px) below `desk`. Other controls keep
their existing sizes.

Topic links in messages and the overview wrap anywhere at every width, so a
long name never widens the reading column or the panel (#974). Below `desk`
a message's topic link and **Select** label are 44 px tall and an overview
topic link fills its row. There a message's time sits under the name and
the topic link and **Select** stay together at the row's end, so at 390 px
a one-line message by a seed member takes that 44 px row and its body line;
a narrower screen or a long name or topic moves them to their own row (#1093, `testMessageRowBrowser` in
`internal/web/message_row_browser_test.go`). From `desk` a message's time
reserves `min-w-timestamp` with tabular digits, wider than the server's UTC
text and the local format of either language, so `message-time-v2.js`
replacing it moves neither the topic link nor **Select** (#1094,
`testMessageTimeBrowser` in `internal/web/message_time_browser_test.go`). Below
`desk` they sit at the row's end, where the time's width moves them only if
it changes how the header wraps; reserving it there would wrap seed rows at 390 px. At every width the create popover may stretch to
16 px above the bottom, capped at its content height, so in a short
viewport (a phone on its side, or a zoomed desktop, #1076) it scrolls to its
submit button.

Message avatars and the account button use the first grapheme of the
display name (via `uniseg`), falling back to the handle when the web layer
blanks a legacy name. Message avatars choose from all six pairs by member ID.
Text precedes the avatar in both page and live markup; a fixed gutter reserves
its space without loading an image or web font.

## Side panels

`sidePanel` renders one `<aside id="side-panel">` for every width, and the
breakpoints ([`ui-code.md`](ui-code.md#dimension-tokens)) place it (#972):

- **From `wide` (1200 px)** a block beside the conversation, `w-panel` wide.
- **From `desk` to `wide`** an overlay: absolute over the conversation's
  right side, with `shadow-popover`, in the conversation's region (`relative`).
- **Below `desk`** a bottom sheet: fixed to the bottom edge, full width,
  rounded only at the top, above the bottom safe area. Its height comes from
  `sheetHeight(panel)`: members and the overview at most 80 %
  (`max-desk:max-h-4/5`), so a later full-height panel, such as the reply
  chain, needs no new structure. The sheet scrolls itself, with
  `overscroll-contain`.

Below `wide` the overlay and the sheet are one modal: `#panel-backdrop` dims
the whole page in `scrim`, a link to the close URL that only a pointer
reaches. The panel covers the conversation exactly while it is out of flow,
which scripts read as its computed `position`: `side-panel-v2.js` makes it
a modal dialog over an inert page ([accessibility](accessibility.md)) and
`read-visibility-v5.js` pauses automatic reading, while the stream still
updates the page behind. The header's **Mark as read** is hidden below
`wide`, where the overview shows a `submitButtonFor` the same
`#read-form`. Without JavaScript the panel is its own page: its links still
close it and page the members.

## Short heights

Below the `short:` height (#1056) a conversation no longer fits its frame:
the header, the composer and the frame's padding take 307 px from `desk`
(291 px below it), so 436 px leaves the list 128 px, `h-32`, a few short
messages. There `#conversation` scrolls as a whole (`short:overflow-y-auto`)
and the list is fixed at `h-32` (`short:flex-none`). It is still the pane the
composer, history and stream scripts move, so they need no change, and
automatic reading never depended on it. The frame keeps `h-dvh`: a frame of
`min-h-dvh` would let the desktop sidebar and a panel beside the
conversation grow to their content and stop scrolling themselves, while
this leaves the sidebar, app bar, panels and list page as they are.

Two things differ there. A covering move form would scroll away from the
composer with the conversation, so the form follows the list in the column
and the composer is hidden; the fixed list still does not resize. A post
scrolls its message into view (`message-stream-v11.js`), which there also
scrolls the conversation up from the composer.

## Arrival motion

The selection pill and the side panel move only on arrival
([motion](ui-code.md#motion), #921). `arrival-v1.js` marks the root
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
cases in the shared `TestBrowser` suite.

Below `desk` a conversation hides the sidebar, so its pill never moves; the
list page's pill grows in when a conversation's back link leads there, and
an opening panel rises as the sheet, because `rb-panel-in` is redefined
under `max-desk:`. `testMotionTouchBrowser`
(`internal/web/motion_touch_browser_test.go`) checks that nothing else moves.
