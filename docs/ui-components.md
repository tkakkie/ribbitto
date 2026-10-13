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
| `submitButton` | `internal/web/view/controls.templ` | a form's submit button, labelled by a message ID: `buttonPrimary` (the main action), `buttonSecondary` (outlined; `buttonSecondaryIcon` adds a decorative child) |
| `textLink` | `internal/web/view/controls.templ` | underlined navigation links; callers supply a message ID and extra typography or spacing classes |
| `textInput` | `internal/web/view/controls.templ` | a single-line text input with a control border and a brand underline on focus, alongside the focus outline; the caller supplies its label |
| `textField` | `internal/web/view/text_field.templ` | a labelled `textInput` with its submit error (setup, sign-up); sign-in passes empty field errors and keeps its form-level alert |
| `iconButton` | `internal/web/view/controls.templ` | a button that shows only an icon (its child) and has an accessible name; can open a popover |
| `welcomeLayout` | `internal/web/view/welcome.templ` | the shared brand and content columns for setup, sign-in, sign-up, home and rate-limit pages |
| `unreadBadge` | `internal/web/view/channel.templ` | an unread count with its screen-reader text |
| `Icon` | `internal/web/view/icon.templ` | a decorative Fluent icon from the vendored sprite ([Icons](ui-code.md#icons)) |
| `Avatar` | `internal/web/view/avatar.templ` | a fixed 36 px round initial; six pastel pairs chosen from the member ID, hidden from assistive technology beside the visible name |
| `historyLink` | `internal/web/view/channel.templ` | the rounded older/newest navigation link; only older links carry the history prepend attributes |
| `sidePanel`, `panelControl` | `internal/web/view/panel.templ` | the white block beside the conversation, with a focusable `h2` and a close link; a header link, labelled or icon-only, that opens one panel, `aria-current="true"` and a Filled icon while it is open |
| `imagePicker` | `internal/web/view/channel_image.templ` | a labelled fieldset of native radios, decorative illustrations and named choices, including the ID-based default; shared by creation and overview |
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

Message avatars and the account button use the first grapheme of the
display name (via `uniseg`), falling back to the handle when the web layer
blanks a legacy name. Text precedes the avatar in both page and live markup;
a fixed gutter reserves its space without loading an image or web font.
