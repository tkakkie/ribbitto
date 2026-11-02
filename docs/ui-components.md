# UI components

Shared templ components ([decision 33](decisions/33-ui-code-shared-components-and-token-utilities-only.md)).
Writing rules: [`ui-code.md`](ui-code.md).

**Keep current:** update this file with shared component changes.

## Shared components

Two uses make a look an `internal/web/view` component; pages reuse it.

| Component | File | Use |
|---|---|---|
| `submitButton`, `submitButtonFor` | `internal/web/view/controls.templ` | submit button labelled by message ID: `buttonPrimary` (the main action), `buttonSecondary` (outlined; `buttonSecondaryIcon` adds a decorative child before the label); `submitButtonFor` submits a form named by its ID from outside it |
| `textLink` | `internal/web/view/controls.templ` | underlined navigation links; callers supply a message ID and extra typography or spacing classes |
| `textInput` | `internal/web/view/controls.templ` | a single-line text input with a control border, brand underline on focus and focus outline; the caller supplies its label |
| `textField` | `internal/web/view/text_field.templ` | a labelled `textInput` with its submit error (setup, sign-up); sign-in passes empty field errors and keeps its form-level alert |
| `iconButton` | `internal/web/view/controls.templ` | icon-only button (its child) with an accessible name; can open a popover |
| `welcomeLayout` | `internal/web/view/welcome.templ` | shared brand/content columns for setup, sign-in, sign-up, home and rate-limit pages; below `desk`, a short brand strip without the composition above the content |
| `signOutButton` | `internal/web/view/hello.templ` | the shared POST sign-out form, taking a button kind; home uses `buttonWelcomeSecondary`, and `SignOutButton` keeps `buttonSecondary` for the account menu |
| `unreadBadge`, `draftMarker` | `internal/web/view/channel.templ` | an unread count with its screen-reader text; `draftMarker` is static draft text, accessible only while not `hidden` |
| `Icon` | `internal/web/view/icon.templ` | a decorative Fluent icon from the vendored sprite ([Icons](ui-code.md#icons)) |
| `Avatar` | `internal/web/view/avatar.templ` | a fixed 36 px round initial; six pastel pairs chosen from the member ID, hidden from assistive technology beside the visible name |
| `messageBody` | `internal/web/view/message_body.templ` | a body: `plain_text` as one pre-wrapped `<p>`; `chat_v1` node by node as paragraphs, quotes, code blocks, bold, italic, code spans, links and member references, each isolated in direction |
| `messageActions` | `internal/web/view/message.templ` | 44 px actions button (#1159): popover from `desk`; below it, a sheet with the button under the avatar |
| `mentionPicker` | `internal/web/view/mention.templ` | the composer's @-picker listbox ([contract](architecture/web-composer.md#composer)) |
| `ComposerQuote` | `internal/web/view/composer_quote.templ` | author, one-line whole excerpt/Quote only part or labelled `quote_text`; hidden ID, 44 px Remove; 422 keeps text/alert; empty without quote; Quote selection opens it; `web/src/composer.ts` sets focus |
| `PinControl` | `internal/web/view/message.templ` | 44 px Pin/Unpin (#890), swapped in place; an alert at the limit |
| `historyLink` | `internal/web/view/channel.templ` | the rounded older/newest navigation link; only older links carry the history prepend attributes |
| `sidePanel`, `panelControl` | `internal/web/view/panel.templ` | the white block beside the conversation, with a focusable `h2` and a close link, covering the conversation below `wide` ([side panels](ui-panels-and-motion.md#side-panels)); a header link, icon-only below `desk` with its accessible name kept, that opens one panel, `aria-current="true"` and a Filled icon while it is open |
| `imagePicker` | `internal/web/view/channel_image.templ` | a labelled fieldset of native radios with the `brand` accent and focus outline, decorative illustrations and named choices, including the ID-based default; shared by creation and overview |
| `channelImage` | `internal/web/view/channel_image.templ` | a decorative channel illustration from a handler-resolved URL; 24 px and lazy in the sidebar, 32 px in the header, with fixed dimensions and asynchronous decoding |

- **Links navigate, buttons act.** Going somewhere is an `<a href>`;
  state changes use a form submit button, even when styled as a link.
- **Components take small inputs** — a label, a kind, a props struct —
  and decide no permissions or data; handlers prepare view models (`docs/architecture/web-layers.md`).
- **A new component arrives with its first user.** Restyling a page also
  extracts its repeated looks.

Welcome forms select `buttonWelcomePrimary` or `buttonWelcomeSecondary`
for 48 px phone targets; `textField` sets `inputProps.Tall` for the same
height. Every `textInput` has `text-entry` (16 px) text below `desk`, so
iOS Safari does not zoom on focus (#1120). The move form's `<select>`
has it too.

Topic links wrap anywhere, keeping messages and the overview within their
columns (#974). Below `desk`
a message's topic link and **Select** label are 44 px tall and an overview
topic link fills its row. There a message's time sits under the name and
the topic link and **Select** stay together at the row's end, so at 390 px
a one-line message by a seed member takes that 44 px row and its body line;
a narrower screen or a long name or topic moves them to their own row, and
when that group is narrower still (as at 320 px with 200 % text, #2048)
**Select** wraps inside it (#1093, `testMessageRowBrowser` in
`internal/web/message_row_browser_test.go`). From `desk` the time reserves `min-w-timestamp` with tabular digits, fitting
UTC and both languages without moving the topic link or **Select** (#1094,
`testMessageTimeBrowser` in `internal/web/message_time_browser_test.go`). Below
`desk` they sit at the row's end, where reserving `min-w-timestamp` would
wrap seed rows at 390 px; there the name-and-time column's `::after`
keeps the server's UTC text laid out, unseen, in the time's grid cell
(`server-time-stack`; a pseudo-element, so the feed's element budget holds),
so the time keeps the larger box of the two and the rewrite never rewraps the header or the time,
at any width, font or text size (#2044, `testMessageTimeSweepBrowser` in
`internal/web/message_time_sweep_browser_test.go`). The message list takes
its height with `flex-auto`: with a zero basis Chromium first sized it at no
height and kept the scrollbar that narrowed and wrapped every header. At every width the create popover may stretch to
16 px above the bottom, capped at its content height, so in a short
viewport (a phone on its side, or a zoomed desktop, #1076) it scrolls to its
submit button.

Exception (#1156): `pendingMessage` mirrors `messageItem`'s layout to keep
the shared stream render unchanged; their different headers and bodies
would complicate a wrapper. `testPendingComposerBrowser` guards geometry
within 1 px. Pending items reserve the 44 px phone header, at full opacity
without motion; Retry, Edit and Remove describe their body. No topic label,
selection or actions menu.

Message avatars and the account button use the first grapheme of the
display name (via `uniseg`), falling back to the handle when the web layer
blanks a legacy name.
Text precedes the avatar in both page and live markup; a fixed gutter reserves
its space without loading an image or web font.

Side panels, short heights and arrival motion are in [`ui-panels-and-motion.md`](ui-panels-and-motion.md).
