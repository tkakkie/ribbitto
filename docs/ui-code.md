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
| `submitButton` | `internal/web/view/controls.templ` | a form's submit button, labelled by a message ID: `buttonPrimary` (the main action), `buttonSecondary` (outlined; `buttonSecondaryIcon` adds a decorative child) |
| `textLink` | `internal/web/view/controls.templ` | underlined navigation links; callers supply a message ID and extra typography or spacing classes |
| `textInput` | `internal/web/view/controls.templ` | a single-line text input with a control border and a brand underline on focus, alongside the focus outline; the caller supplies its label |
| `textField` | `internal/web/view/text_field.templ` | a labelled `textInput` with its submit error (setup, sign-up); sign-in passes empty field errors and keeps its form-level alert |
| `iconButton` | `internal/web/view/controls.templ` | a button that shows only an icon (its child) and has an accessible name; can open a popover |
| `welcomeLayout` | `internal/web/view/welcome.templ` | the shared brand and content columns for setup, sign-in, sign-up, home and rate-limit pages |
| `unreadBadge` | `internal/web/view/channel.templ` | an unread count with its screen-reader text |
| `Icon` | `internal/web/view/icon.templ` | a decorative Fluent icon from the vendored sprite ([Icons](#icons)) |
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

## Sidebar order and position

Channels are listed by name, and the current channel nests its topics: the
default first, then by name, at most 50 ([`topics.md`](domain/topics.md)).
The order never changes to bring the current row forward (#922). Instead
`sidebar-scroll-v1.js`, once on load, scrolls the sidebar's own pane (the
`<aside>` around `#sidebar-channels`) so that the target row is in view,
centred, when it is not already: the selected topic's row when it is
listed, otherwise the current channel's row (an unlisted topic, or the
feed). It moves neither the window nor focus, and scrolls instantly.
Live recounts replace `#sidebar-channels` inside that pane, which keeps its
scroll position, so the script does nothing then: the member's position
wins. Without JavaScript the sidebar opens at its top.

## Styles

- **Templates use token utilities only.** Colours, sizes, radii and shadows
  come from tokens in `web/styles/app.css`. A utility with an arbitrary
  value or variant (anything in square brackets, such as `w-[760px]` or
  `[&>li]:p-1`) is never used: add a token and document it instead.
  `tools/sourcecheck` rejects square brackets in template class
  attributes, including string literals in dynamic class expressions
  (comments are skipped), and reports each such class once — under the
  colour rule below when it holds a raw colour; computed class names are
  not evaluated, so reviewers check those.
- **No literal colours.** `tools/sourcecheck`, run by `make check`, rejects
  literal colours in arbitrary classes and inline styles in authored
  `.templ` attributes, and in `web/styles/app.css` (including `@apply`)
  outside token definitions (`--rb-*` in `:root`, and custom properties in
  `@theme`). Token references, `transparent` and `currentColor` pass; URL
  contents (quoted or unquoted) are ignored before arbitrary property
  prefixes are stripped. Generated CSS and vendored assets are excluded.
  The pattern check does not evaluate dynamic expressions.
- **`web/styles/app.css` holds only** the tokens (`:root` colours and
  `@theme`), base styles, and what utilities cannot express: `@keyframes`,
  the arrival animations and the reduced-motion rule. No component classes and
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
| `w-panel` | 340 px | a side panel (#874); room for an overview image and a members list with names and handles |
| `max-w-reading` | 760 px | the conversation's reading column |
| `rounded-xl` | 12 px | the rounded blocks |
| `shadow-block` | a 1 px ring and a soft drop | blocks and cards |
| `shadow-popover` | a deeper drop | menus, popovers and dialogs |

**Layers.** Only four `z-index` values are used, through Tailwind's
built-in utilities, so no z-index tokens are added: `z-10` for something
sticky inside a block, `z-20` for a panel over content, `z-30` for menus
and popovers, `z-40` for dialogs.

## Motion

Small and optional ([`ui.md`](ui.md#tokens)). Perceived speed comes first:
motion never delays readable text or blocks input, and nothing loops except
the typing dots.

| Token | Value | Use |
|---|---|---|
| `--duration-faster` | 100 ms | hover and press feedback (the default for `transition-feedback`); the selection pill |
| `--duration-fast` | 150 ms | an opening panel |
| `--duration-normal`, `--duration-gentle` | 200 ms, 250 ms | reserved for larger surfaces |
| `ease-standard` | `cubic-bezier(0.33, 0, 0.67, 1)` | colour changes |
| `ease-decelerate` | `cubic-bezier(0, 0, 0, 1)` | things that enter |
| `ease-accelerate` | `cubic-bezier(0.8, 0, 0.78, 1)` | things that leave |

- **Rows and buttons** add `transition-feedback motion-safe:active:scale-96` (a utility in `app.css` that animates only colours and `scale`, rather than Tailwind's longer `transition` list, #921): colours
  change over 100 ms and a press scales to 0.96.
- **Messages never move**, live ones included: the page cannot tell
  reconnect backlog from live delivery, and backlog must not move.
- **The typing dots** pulse (`animate-typing-dot-1`…`3`) while someone types.
- **Navigation** has no cross-document view transition: its setup delayed
  every switch (#921). The new page shows from its first frame. Only an
  opening panel slides in 8 px over 150 ms (a closing one just disappears),
  and the pill grows into a new row over 100 ms.
- **Arrival.** `arrival-v1.js` marks the root (`data-arrive-pill`,
  `data-arrive-panel`) and CSS animates under those marks. It reads
  `navigation.activation` and stores nothing: only a `push` whose previous
  entry is same-origin (a link, or a form's redirect) is marked, the pill
  when the place (the path without `/members` or `/overview`) changed, the
  panel when one newly opened. A first load, a reload or Back/Forward is
  never marked; neither is a panel switch or close. A mark is removed when
  its animation ends, the pill's earlier at the first live
  `#sidebar-channels` replacement, so a live list never grows its pill
  again; one with nothing to animate (reduced motion, a hidden row) is
  removed at `DOMContentLoaded`. The script sits in `<head>` and is not
  deferred: it must mark the page before the first paint, or the pill
  shows at rest first. Browsers without the Navigation API show no arrival
  motion. `testArrivalBrowser` (`internal/web/arrival_browser_test.go`)
  checks these cases in the shared `TestBrowser` suite.
- **Reduced motion.** The arrival animations and press scaling exist only
  under `prefers-reduced-motion: no-preference`, and under `reduce` one rule
  removes every animation and transition. `TestReducedMotion`
  (`web/static/motion_test.go`) checks this on the built stylesheet, and
  `TestNoViewTransitions` that it declares no view transition.

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
never supplies its accessible name. “Load older” uses the arrow-up
icon and keeps its text and paging contract.
