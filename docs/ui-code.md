# UI code

How templates and styles are written so that pages built by different
people and AIs still look like one product ([decision 33](decisions/33-ui-code-shared-components-and-token-utilities-only.md)).
What the UI looks like is in [`ui.md`](ui.md); markup and accessibility
rules are in [`accessibility.md`](accessibility.md).

**Keep it current:** update this file in the same pull request when a
dimension token or one of these rules changes. The shared components are
in [`ui-components.md`](ui-components.md).

## Sidebar order and position

Channels are listed by name, and the current channel nests its topics: the
default first, then by name, at most 50 ([`topics.md`](domain/topics.md)).
The order never changes to bring the current row forward (#922). Instead
`sidebar-scroll-v2.js`, once on load, scrolls the sidebar's own pane (the
`<aside>` around `#sidebar-channels`, or the list page's `<main>`) so that the target row is in view,
centred, when it is not already: the selected topic's row when it is
listed, otherwise the current channel's row (an unlisted topic, or the
feed). It moves neither the window nor focus, and scrolls instantly.
Live recounts replace `#sidebar-channels` inside that pane, which keeps its
scroll position, so the script does nothing then: the member's position
wins. Without JavaScript the sidebar opens at its top, unless the URL's
fragment names a row. The channel list page
(`/organizations/{slug}/channels?channel=<id>`, #969) is the sidebar's
content as the page's `<main>`, with the same pane, script and recounts;
below `desk` the app bar and the block that prompts to open a channel at
desktop width are hidden, and the list fills the screen above the bottom
navigation (#970), the frame's last row, so no row scrolls behind it.
There a conversation hides the app bar and the sidebar and shows no bottom
navigation; its header's back link carries the channel's row id as its
fragment, so the row is in view even without JavaScript.

The account menu is one popover (`#account-menu`) in the frame, opened by
the app bar's button at desktop width and the bottom navigation's Account
below `desk`, where it is a bottom sheet. It sits outside both bars because
a popover inside an ancestor hidden at the current width cannot show.

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
- **`web/styles/app.css` holds only** the tokens (`:root` colours, shadows and
  `@theme`), base styles, and what utilities cannot express: `@keyframes`,
  arrival animations, reduced motion, sizing, safe-area and anchor utilities.
  No component classes and
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
| `min-w-timestamp` | 22 ch | a message's time from `desk` ([why](ui-components.md#shared-components)) |
| `rounded-xl` | 12 px | the rounded blocks |
| `shadow-block` | a 1 px ring and a soft drop in light; a faint 1 px ring in dark | blocks and cards |
| `shadow-popover` | a deeper drop in light; a faint 1 px ring in dark | menus, popovers and dialogs |

The shadow utilities read `--rb-shadow-block` and `--rb-shadow-popover`,
defined in `:root` and overridden in the dark colour scheme.

**Breakpoints** ([`ui.md`](ui.md#layout-principles), #966): below 852 px
(53.25 rem) the phone layout; from 852 px the desktop frame with a side
panel overlaying the conversation; from 1200 px (75 rem) the panel beside
it. The first is the `@theme` breakpoint `--breakpoint-desk` (#969): `desk:`
utilities apply from 852 px and `max-desk:` below it, so phone overrides
leave the desktop classes as they are. The second is `--breakpoint-wide`
(#972): below it (`max-wide:`) an open panel is a modal over the dimmed
page ([side panels](ui-components.md#side-panels)). The custom variant
`short:` (#1056) applies below 27.25 rem (436 px) tall, where the header and
composer leave under 128 px for the list ([short heights](ui-components.md#short-heights)).

**Safe area** (#970). The viewport meta has `viewport-fit=cover`, without
which `env(safe-area-inset-*)` stays 0, so pages extend under a phone's
home indicator and, in landscape, its notch. The base style pads `body`
by the side insets, as the default fit did, on a `bg-base` body. What meets the bottom edge
below `desk` uses `pb-safe-<n>`, `n` spacing steps plus the bottom inset:
`pb-safe-0` on the bottom navigation and the panel sheet, `pb-safe-4` on
the account sheet, composer and welcome pages (`pb-safe-3` on the move form).

**Phone conversation** (#971). `max-desk:` overrides remove the frame
padding and conversation radius, give messages and composer 16 px gutters,
and hide only header text labels, keeping the same sprite icons and
accessible names at both widths. The topic's channel link keeps a 44 px
target; both names truncate on one row. Plain-text bodies (URLs and code
included) and metadata use `wrap-anywhere` below `desk`, topic links at
every width (#974).
Every text input, textarea and select uses `text-entry` (16/22 px; not `text-input`, which is the `input` colour) below `desk` to avoid
iOS focus zoom (#973, #1120). The send button stays 44 × 44 px.
Only latest feed and topic pages are enhanced;
history submissions remain plain forms. `message-composer-v14.js`
also guards Enter within 100 ms of `compositionend`, since phone IMEs can
clear `isComposing` before the confirming keydown.

The viewport also requests `interactive-widget=resizes-content`: supporting
browsers resize the existing `h-dvh` frame for the keyboard, leaving the
composer outside the message pane. No keyboard JavaScript is added;
browsers that ignore the key still need device/emulation verification.

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
- **Hover styles exist only under `@media (hover: hover)`:** a tap leaves
  its element `:hover`, which a touch screen must not show; the press still
  does. Tailwind's `hover:` does this; a hand-written rule must too
  (`TestHoverOnlyWithHover`).
- **Messages never move**, live ones included: the page cannot tell
  reconnect backlog from live delivery, and backlog must not move.
- **The typing dots** pulse (`animate-typing-dot-1`…`3`) while someone types.
- **Navigation** has no cross-document view transition: its setup delayed
  every switch (#921). The new page shows from its first frame. Only an
  opening panel slides in 8 px over 150 ms, a phone's sheet up 16 px
  (a closing one just disappears), and the pill grows into a new row over
  100 ms, the list page's too. Their keyframes only move, never fade, and
  no motion has a delay (`TestArrivalMotion`).
- **Arrival.** `arrival-v1.js` marks a page reached from another page of
  the app, and CSS animates under the marks ([arrival
  motion](ui-components.md#arrival-motion)).
- **Reduced motion.** The arrival animations and press scaling exist only
  under `prefers-reduced-motion: no-preference`, and under `reduce` one rule
  removes every animation and transition. `TestReducedMotion`
  (`web/static/motion_test.go`) checks this on the built stylesheet (the
  rule misses pseudo-elements such as `::backdrop`, so their motion needs
  `no-preference`), and `TestNoViewTransitions` that it declares no view
  transition. `testMotionTouchBrowser` taps rows and buttons at phone width
  under touch emulation, in light, dark and reduced motion.

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
