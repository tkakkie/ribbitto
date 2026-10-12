# UI

How ribbitto looks and why. Read this before building a screen.

**Keep it current:** update this file in the same pull request whenever a
token, the contrast table or a layout principle changes. The tokens
themselves live only in `web/styles/app.css`; this file explains them.

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
- **Light only until M6.** M6 designs dark colours for this direction;
  until then pages use the light theme whatever the system setting, so the
  old dark theme never mixes with the new light one. All colours are CSS
  variables, so dark mode is one override block.

## Tokens

Defined in `web/styles/app.css` and used through Tailwind utilities
(`bg-base`, `text-muted`, `border-border-strong`, `rounded-md`,
`text-body`, …). Tailwind's default colour palette is switched off, so only
these colours exist in templates. `tools/sourcecheck`, run by `make check`,
rejects literal colours in arbitrary classes and inline styles in authored
`.templ` attributes, and in `web/styles/app.css` (including `@apply`) outside
token definitions (`--rb-*` in `:root`, and custom properties in `@theme`). Token references,
`transparent` and `currentColor` pass; URL contents (quoted or unquoted) are
ignored before arbitrary property prefixes are stripped. Generated CSS and
vendored assets are excluded. The pattern check does not evaluate dynamic expressions.

| Colour token | Value | Use |
|---|---|---|
| `base` | `#edf4f2` | page ground; the sidebar sits on it |
| `bg` | `#ffffff` | blocks: the conversation, panels |
| `rail` | `#ffffff` | the app bar block |
| `sidebar` | `#edf4f2` | the channel sidebar (the base) |
| `panel` | `#f5f9f8` | the inside of a side panel, behind its cards |
| `surface` | `#ffffff` | cards, popovers |
| `input` | `#ffffff` | text fields |
| `hover` | `#e4edeb` | hovered rows and buttons |
| `border` | `#e1e8e6` | decorative dividers only |
| `border-strong` | `#7f8986` | borders that identify a control (inputs) |
| `fg` | `#171c1b` | body text |
| `muted` | `#545d5b` | secondary text, timestamps |
| `brand` / `on-brand` | `#0f766e` / `#ffffff` | primary buttons, focus ring, selection indicator, unread counts, links |
| `selected` / `on-selected` | `#d5f1ea` / `#0b4d48` | the current row |
| `accent` / `on-accent` | `#fff0e6` / `#8a3a12` | the "New" divider's label |
| `accent-line` | `#f2a37b` | the "New" divider's line (decorative; the label carries the meaning) |
| `success` | `#3a9742` | online dot (always with a label) |
| `warning` | `#8a5300` | warning text and icons (e.g. "reconnecting…") |
| `danger` / `on-danger` | `#b42318` / `#ffffff` | error text; destructive buttons |
| `pastel-1`…`pastel-6` / `on-pastel-1`…`on-pastel-6` | `#dcebff`/`#1f5aa6`, `#fff3c4`/`#7a5d00`, `#d9f2e6`/`#1d6b47`, `#ffe4d6`/`#9a4a1f`, `#ebe3ff`/`#5b3fa8`, `#ffe0ea`/`#a3284f` | channel images and avatar fills / their initials |

| Other token | Value |
|---|---|
| Fonts | `font-sans`: system UI fonts with Japanese fallbacks (Hiragino Sans, Noto Sans JP, Yu Gothic, Meiryo); `font-mono` for code. No web fonts (self-hosting and privacy). |
| Type scale | `text-caption` 12/16 · `text-body` 14/22 · `text-title` 15/22 · `text-heading` 20/28 · `text-display` 28/36 (px, size/line height) |
| Spacing | `--spacing: 0.25rem` — a 4 px grid (`p-1` = 4 px, `p-4` = 16 px) |
| Radius | `rounded-sm` 4 px · `rounded-md` 6 px · `rounded-lg` 10 px · `rounded-xl` 12 px (blocks) |
| Shadow | `shadow-block` (blocks, cards) · `shadow-popover` (menus, dialogs) |
| Focus | 2 px `brand` outline, 2 px offset, on `:focus-visible` |

The layout widths (`w-rail`, `w-sidebar`, `w-panel`, `max-w-reading`),
the shared components and how templates and styles are written are in
[`ui-code.md`](ui-code.md); motion tokens come with #878.

## Contrast (WCAG 2.2 AA)

Normal text, including button labels, links and typed text, needs 4.5:1;
large text — at least 24 px regular or 18.66 px bold (18 pt / 14 pt), or
the equivalent size for Japanese text — needs 3:1; non-text indicators that convey state (focus ring, input borders,
selection indicator, online dot) 3:1 against what is next to them. Ratios are
truncated, never rounded up. Decorative `border` and `accent-line` carry no
information and are exempt; a control's boundary always uses
`border-strong`. The current row is shown by `on-selected` text **and** the
`brand` indicator, not by the `selected` fill alone. `bg`, `rail`, `surface`
and `input` share one value, and `sidebar` equals `base`, so each row below
covers them.

| Pair (foreground on background) | Used for | Needed | Ratio |
|---|---|---|---|
| `fg` on `bg` / `base` / `panel` / `hover` | body text | 4.5:1 | 17.23 / 15.44 / 16.24 / 14.45 |
| `muted` on `bg` / `base` / `panel` / `hover` | secondary text | 4.5:1 | 6.78 / 6.08 / 6.39 / 5.69 |
| `on-brand` on `brand` | button label, unread count | 4.5:1 | 5.47 |
| `on-selected` on `selected` | current row's label | 4.5:1 | 8.11 |
| `on-accent` on `accent` | "New" divider label | 4.5:1 | 6.99 |
| `brand` on `bg` / `base` | link text; focus ring | 4.5:1 | 5.47 / 4.90 |
| `brand` on `selected` | indicator on the current row | 3:1 | 4.58 |
| `success` on `bg` / `base` | online dot | 3:1 | 3.69 / 3.31 |
| `warning` on `bg` / `base` | warning text | 4.5:1 | 6.32 / 5.67 |
| `danger` on `bg` / `base` | error text | 4.5:1 | 6.57 / 5.89 |
| `on-danger` on `danger` | destructive button label | 4.5:1 | 6.57 |
| `border-strong` on `bg` / `base` | input border | 3:1 | 3.60 / 3.23 |
| `on-pastel-n` on `pastel-n` (1…6) | initials on avatars | 4.5:1 | 5.65 / 5.56 / 5.48 / 5.13 / 6.23 / 5.75 |

## Layout principles

- **Desktop:** the app bar block (logo, Home, the account menu) · the
  channel sidebar on the base · the conversation block · a side panel block
  when one is open.
- **Conversation:** a thin header; a centred reading column about 760 px
  wide with 15 px text; a rounded composer block at the bottom.
- **Mobile** (M6): one column — channel header, messages, composer, bottom
  navigation. Every tap target is at least 44 × 44 px.
- **Text first.** A message is readable before anything around it (avatars,
  images) has loaded.

## Product labels

The frog labels (`docs/domain/vocabulary.md`) are names. Their meaning
comes from presentation and placement, never from ordinary words added
beside them (decisions 20 and 28):

- **Placement shows the hierarchy.** The organisation's name and image sit
  at the top of the sidebar; channels are listed under it, each with its
  image; a channel's topics are nested under it on a tree line, with the
  default topic first; the conversation header repeats the path
  (`channel › topic`).
- **Each kind has one look.** Channels always show their image; topics a
  small branch icon; messages an avatar, name and time. A label is never
  shown without its look.
- **Labels name things; verbs stay ordinary.** A label appears as the
  noun for its thing (headings, counts, placeholders); the action beside it
  uses an ordinary word ("Create", "Send"). Japanese uses the same
  Latin-script word.

Which strings use a label is applied in #879.

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
  frog-like micro-animations — as long as it is cheap to render and turned
  off under `prefers-reduced-motion`.
