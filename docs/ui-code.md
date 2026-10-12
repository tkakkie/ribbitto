# UI code

How templates and styles are written so that pages built by different
people and AIs still look like one product ([decision 33](decisions/33-ui-code-shared-components-and-token-utilities-only.md)).
What the UI looks like is in [`ui.md`](ui.md); markup and accessibility
rules are in [`accessibility.md`](accessibility.md).

**Keep it current:** update this file in the same pull request when a
shared component, a dimension token or one of these rules changes.

## Shared components

A look used in two places becomes a component in `internal/web/view`, and
pages compose components instead of repeating a control's class string.
Today's shared controls:

| Component | File | Use |
|---|---|---|
| `submitButton` | `internal/web/view/controls.templ` | a form's submit button, labelled by a message ID: `buttonPrimary` (the main action), `buttonSecondary` (outlined; `buttonSecondaryIcon` adds a decorative child), `buttonLink` (looks like a link, still submits) |
| `textInput` | `internal/web/view/controls.templ` | a single-line text input with a control border and a brand underline on focus, alongside the focus outline; the caller supplies its label |
| `textField` | `internal/web/view/text_field.templ` | a labelled `textInput` with its submit error (setup, sign-up); sign-in passes empty field errors and keeps its form-level alert |
| `iconButton` | `internal/web/view/controls.templ` | a button that shows only an icon (its child) and has an accessible name; can open a popover |
| `welcomeLayout`, `welcomeLink` | `internal/web/view/welcome.templ` | the shared brand and content columns for setup, sign-in, sign-up, home and rate-limit pages, and their navigation links |
| `unreadBadge` | `internal/web/view/channel.templ` | an unread count with its screen-reader text |
| `Icon` | `internal/web/view/icon.templ` | a decorative Fluent icon from the vendored sprite ([Icons](#icons)) |
| `Avatar` | `internal/web/view/avatar.templ` | a fixed 36 px round initial; six pastel pairs chosen from the member ID, hidden from assistive technology beside the visible name |
| `historyLink` | `internal/web/view/channel.templ` | the rounded older/newest navigation link; only older links carry the history prepend attributes |
| `sidePanel`, `panelControl` | `internal/web/view/panel.templ` | the white block beside the conversation, with a focusable `h2` and a close link; a labelled header link that opens one panel, `aria-current="true"` and a Filled icon while it is open |
| `channelImage` | `internal/web/view/channel_image.templ` | a decorative channel illustration from a handler-resolved URL; 24 px and lazy in the sidebar, 32 px in the header, with fixed dimensions and asynchronous decoding |

- **Links navigate, buttons act.** Going somewhere is an `<a href>`;
  anything that changes state is a submit button in a form, even when it
  should look like a link (`buttonLink`).
- **Components take small inputs** — a label, a kind, a props struct —
  and decide nothing about permissions or data; handlers prepare view
  models (`docs/architecture/web-layers.md`).
- **A new component arrives with its first user.** The account menu came with #872 (`appBar`, using an HTML popover), the avatar with
  #873, the panel with #874, the channel image with #875. When a page is
  restyled, its repeated looks (links, for example) become components too.

Message avatars use the first grapheme of the display name (via `uniseg`),
falling back to the handle when the web layer blanks a legacy name. Text
precedes the avatar in both page and live markup; a fixed gutter reserves
its space without loading an image or web font.

## Styles

- **Templates use token utilities only.** Colours, sizes, radii and shadows
  come from tokens in `web/styles/app.css`. A utility with an arbitrary
  value or variant (anything in square brackets, such as `w-[760px]` or
  `[&>li]:p-1`) is never used: add a token and document it instead.
  `tools/sourcecheck` rejects square brackets in template class
  attributes, including string literals in dynamic class expressions
  (comments are skipped), and reports each such class once — under the
  colour rule in [`ui.md`](ui.md) when it holds a raw colour; computed
  class names are not evaluated, so reviewers check those.
- **`web/styles/app.css` holds only** the tokens (`:root` colours and
  `@theme`), base styles, and what utilities cannot express: `@keyframes`,
  view transitions and the reduced-motion rule. No component classes and
  no `@apply` for components; no `style` attributes in templates.
- **Spacing** follows the 4 px grid; a container sets the space between
  its children with `gap`, rather than margins on each child.

## Dimension tokens

The fixed widths of the desktop layout, and the block shape
([`ui.md`](ui.md#layout-principles)):

| Utility | Value | Use |
|---|---|---|
| `w-rail` | 76 px | the app bar block |
| `w-sidebar` | 264 px | the channel sidebar |
| `w-panel` | 340 px | a side panel (#874); room for an overview banner and a members list with names and handles |
| `max-w-reading` | 760 px | the conversation's reading column |
| `rounded-xl` | 12 px | the rounded blocks |
| `shadow-block` | a 1 px ring and a soft drop | blocks and cards |
| `shadow-popover` | a deeper drop | menus, popovers and dialogs |

**Layers.** Only four `z-index` values are used, through Tailwind's
built-in utilities, so no z-index tokens are added: `z-10` for something
sticky inside a block, `z-20` for a panel over content, `z-30` for menus
and popovers, `z-40` for dialogs.

## Icons

Use the shared `Icon` templ component in `internal/web/view/icon.templ`:
`@Icon("arrow-up-regular", 20)`. Names are string literals, checked against
the embedded sprite even in templates not rendered by page fixtures. The
size is in CSS pixels; width and height reserve the space before loading.
Icons inherit text colour through `currentColor` and never need inline CSS.
Use Regular at 20 px normally; Filled is reserved for a selected state.
The available names, pinned source, licence and rebuild steps are in
[`web/static/vendor/README.md`](../web/static/vendor/README.md).

Icons are decorative (`aria-hidden="true"`, `focusable="false"`). Keep a
control's text label, or give an icon-only button an `aria-label`; the icon
never supplies its accessible name. “Load older messages” uses the arrow-up
icon and keeps its text and paging contract.
