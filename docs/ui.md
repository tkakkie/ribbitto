# UI

How ribbitto looks and why. Read this before building a screen.

**Keep it current:** update this file in the same pull request whenever a
token or a layout principle changes; colours and contrast are in
[`ui-colours.md`](ui-colours.md). The tokens themselves live only in
`web/styles/app.css`; these files explain them.

## Direction

Chosen by the maintainer from mock-ups for M5 (#870), replacing
"B · Clear water" from #9: white rounded blocks on a lightly tinted base,
with the calm reading column of a modern AI chat app, the order of a tool
like Linear, and on desktop the rounded, layered feel of Microsoft's Fluent 2.
The mock-ups are a direction, not a specification.

- **Blocks on a base.** The page ground is the tinted `base`. The app bar,
  the conversation, and a side panel when one is open, are white rounded
  blocks (12 px radius, 8 px gaps, one light shadow); the channel sidebar
  sits directly on the base, so its edges are the blocks' edges and it
  needs no divider. The current navigation row shows a short rounded
  (pill) indicator in `brand`, as Fluent 2 does.
- **Neutrals lean toward the brand.** Every grey carries a trace of the
  brand hue (OKLCH hue about 180, chroma at most 0.012), so greys and teal
  look like one family. Blocks stay pure white, so they lift off the base
  and images keep their colours.
- **Three kinds of colour.** One deep **brand** teal, used sparingly:
  primary actions, focus, the selection indicator, unread counts. A pale
  tint of it, **selected**, for the current row. A family of six **pastels**
  for channel images and avatars, the only place where hue varies freely.
- **Meaning stays apart from the brand.** The online dot is a yellower
  green than the teal; the "New" divider is warm, opposite the teal. No
  state relies on colour alone: it also has text, an icon or a shape.
- **Readable first.** The sidebar and headers are compact; the reading
  column is calm and generous.
- **Dark follows the system** (#967): pages follow
  `prefers-color-scheme` without a reload; a manual switch comes later.
  Blocks lift by lightness, not shadow: the base is the darkest surface,
  blocks are slightly lighter, with a faint hairline instead of the shadow,
  and greys keep the brand's hue trace. One brighter teal serves the brand
  fill, links, focus and the selection indicator, with dark text on filled
  controls. Images keep their colours (later photos are never recoloured);
  avatar initials use darker pastel fills with light initials. All colours
  are CSS variables, so dark mode is one override block.

## Tokens

Defined in `web/styles/app.css` and used through Tailwind utilities
(`bg-base`, `text-muted`, `border-border-strong`, `rounded-md`,
`text-body`, …).
Colour tokens, with their light and dark values,
and the contrast table are in [`ui-colours.md`](ui-colours.md).

| Other token | Value |
|---|---|
| Fonts | `font-sans`: system UI fonts with Japanese fallbacks (Hiragino Sans, Noto Sans JP, Yu Gothic, Meiryo); `font-mono` for code. No web fonts (self-hosting and privacy). |
| Type scale | `text-caption` 12/16 · `text-body` 14/22 · `text-title` 15/22 · `text-entry` 16/22 (phone composer) · `text-heading` 20/28 · `text-display` 28/36 (px, size/line height) |
| Spacing | `--spacing: 0.25rem` — a 4 px grid (`p-1` = 4 px, `p-4` = 16 px) |
| Radius | `rounded-sm` 4 px · `rounded-md` 6 px · `rounded-lg` 10 px · `rounded-xl` 12 px (blocks) |
| Shadow | `shadow-block` (blocks, cards) · `shadow-popover` (menus, dialogs) |
| Focus | 2 px `brand` outline, 2 px offset, on `:focus-visible` |
| Motion | durations 100 · 150 · 200 · 250 ms; easings `standard`, `decelerate` (entering), `accelerate` (leaving), after Fluent 2 |

The layout widths (`w-rail`, `w-sidebar`, `w-panel`, `max-w-reading`)
and how templates and styles are written are in [`ui-code.md`](ui-code.md);
the shared components are in [`ui-components.md`](ui-components.md).

**Motion** animates only `transform`, `opacity` and colours, never delays
readable text, and is off under `prefers-reduced-motion: reduce`; uses:
[Motion](ui-code.md#motion).

## Layout principles

- **Welcome pages:** setup, sign-in, sign-up, home and the rate-limit page
  share a rounded card on the base. On desktop, the logo and tagline sit
  on the pale `selected` brand tint beside the white content column; a
  decorative row of four channel illustrations sits below them.
- **Desktop:** the app bar block (logo, Home, the account menu) · the
  channel sidebar on the base · the conversation block · a side panel block
  when one is open.
- **Conversation:** a thin header; a centred reading column about 760 px
  wide with 15 px text; a rounded composer block at the bottom, whose
  place the move form takes while a message is checked.
- **Every page** (M6, #966): setup, sign-in, sign-up, home `/`, the
  channel feed and the topic view with the members and overview panels,
  the rate-limit page, and the channel list page (#969). 404 and
  500 stay plain-text responses.
- **Widths** (measured for #966): the reading column stays comfortable
  down to 480 px at 15 px text. The frame takes 372 px, and 720 px with a
  side panel, so from 1200 px (75 rem) a panel sits beside the
  conversation; from 852 px (53.25 rem) a panel overlays the conversation's
  right side instead; below 852 px the phone layout applies. From 320 px
  up nothing scrolls sideways and no control becomes unreachable.
- **Phone** (M6): one column. The channel list is its own page with a
  bottom navigation (#970) instead of the app bar: Home, and an Account
  button that opens the account menu as a bottom sheet, each an icon above
  a short label, both above the safe area. A conversation fills the screen without the
  bottom navigation, with 16 px message gutters. Its one-row header keeps
  the back link, channel image, truncated channel/topic names, and named
  icon controls for **Mark as read**, Members and Overview (#971).
  The composer sits above the bottom safe area, with 16 px input text.
  Members and the overview will open as bottom sheets, with **Mark as
  read** in the overview sheet (#972). Welcome pages show a short
  brand strip (logo, name, tagline) above the form, without the
  illustrations.
- **Tap targets** at phone width are at least 44 × 44 px: links, buttons,
  form controls, `<summary>` and labels that act as a control's target.
  Links inside running text (message and help text) are the one
  exception. Desktop keeps WCAG 2.2 AA (24 px, 2.5.8).
- **Text first.** A message is readable before anything around it (avatars,
  images) has loaded.

## Product labels

The frog labels (`docs/domain/vocabulary.md`) are names. Their meaning
comes from presentation and placement, never from ordinary words added
beside them (decisions 20 and 28):

- **Placement shows the hierarchy.** The organisation's name sits at the
  top of the sidebar (an organisation image is not built yet); channels
  are listed under it, each with its image; a channel's topics are nested under it on a tree line, with the
  default topic first; the conversation header repeats the path
  (`channel › topic`).
- **Each kind has one look.** Channels always show their image; topics a
  small branch icon; messages an avatar, name and time. A label is never
  shown without its look.
- **Labels name things; verbs stay ordinary.** A label appears as the
  noun for its thing (headings, counts, placeholders); the action beside it
  uses an ordinary word ("Create", "Send"). Japanese uses the same
  Latin-script word.

The strings follow this on the conversation screen, where each kind's
look is present; welcome pages keep ordinary words, and errors, help and
notices name no thing ([`vocabulary.md`](domain/vocabulary.md#product-labels)).

## Brand

The logo is at least 16 px, with clear space of a quarter of its size, in
brand teal, white on a teal tile, or `currentColor` only. The mascot is
meant for welcome pages, empty states and loading or sending, but no page
places it yet (the welcome card shows channel illustrations); it is
decorative (`alt=""`) and never the only carrier of information. Files,
poses, the channel illustrations and details are in [`brand.md`](brand.md).

## Markup and accessibility

The markup and accessibility rules, which `TestPagesMarkup` checks, and how to check a UI
change in a browser are in [`accessibility.md`](accessibility.md).

## Planned directions (from the maintainer, #9)

Recorded now, designed when each screen is built:

- **Channels** use the flat chat layout above. **Direct messages** (after the
  MVP) use **speech bubbles**, so they feel like a conversation.
- Channel entries show an **image** instead of `#` (illustrations in M5,
  #875; photos once uploads exist), and images are welcome elsewhere to
  make the app feel fun.
- **Performance comes first.** Images never delay messages: they are small
  and fixed-size with `width`/`height` set (no layout shift), lazy-loaded and
  decoded asynchronously, and cached as immutable; the text of a message
  renders immediately and images fill in after.
- **Playfulness** in design or motion is welcome — for example small,
  frog-like micro-animations — under the motion rule above.
