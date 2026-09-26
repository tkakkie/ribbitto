# UI

How ribbitto looks and why. Read this before building a screen.

**Keep it current:** update this file in the same pull request whenever a
token, the contrast table or a layout principle changes. The tokens
themselves live only in `web/styles/app.css`; this file explains them.

## Direction

The maintainer chose **"B · Clear water"** from the mock-ups on #9: crisp and
dense, mostly white and grey with thin borders, small radii and a leaf-green
brand colour — in the spirit of modern SaaS tools (Linear, Vercel, Stripe)
and Microsoft's desktop sites. The mock-ups are a direction, not a
specification: each screen's details are decided when it is built.

- **Neutral first.** Greys carry the layout; the brand green is reserved for
  primary actions, the current selection and focus.
- **Two greens that never meet.** The brand green leans yellow (leaf, frog);
  the success colour (the "online" dot) leans blue. Neither is ever the only
  signal: states also have text, an icon or a shape.
- **Mobile is its own layout** (full-width conversation, bottom navigation),
  not a shrunk desktop page.
- **Dark mode follows the system** (`prefers-color-scheme`). All colours are
  CSS variables, so a manual switch can be added later without touching
  components.

## Tokens

Defined in `web/styles/app.css` and used through Tailwind utilities
(`bg-surface`, `text-muted`, `border-border-strong`, `rounded-md`,
`text-body`, …). Tailwind's default colour palette is switched off, so only
these colours exist in templates.

| Colour token | Light | Dark | Use |
|---|---|---|---|
| `bg` | `#ffffff` | `#0f1112` | page background |
| `rail` | `#f1f3f2` | `#0a0b0c` | workspace rail |
| `sidebar` | `#f7f8f7` | `#141718` | channel sidebar, mobile bottom bar |
| `surface` | `#ffffff` | `#171a1b` | cards, popovers |
| `input` | `#f7f8f7` | `#141718` | text fields |
| `border` | `#e3e6e4` | `#26292b` | decorative dividers only |
| `border-strong` | `#8a9096` | `#6b7278` | borders that identify a control (inputs) |
| `fg` | `#16181a` | `#e6e8ea` | body text |
| `muted` | `#595f66` | `#9aa1a8` | secondary text, timestamps |
| `brand` / `on-brand` | `#4a7a0f` / `#ffffff` | `#9ccc5a` / `#10170a` | primary buttons, focus ring, selection bar |
| `selected` / `on-selected` | `#e9f1de` / `#2f4f08` | `#1f2a14` / `#d6ecb8` | the current channel |
| `accent` / `on-accent` | `#fbf3d5` / `#5c4a0c` | `#3a3316` / `#fbf3d5` | "New" divider, gentle highlights |
| `success` | `#0e7490` | `#5fc3dc` | online dot (always with a label) |
| `warning` | `#8a5300` | `#f0b35a` | warning text and icons (e.g. "reconnecting…") |
| `danger` / `on-danger` | `#b42318` / `#ffffff` | `#ff8a7a` / `#1a0806` | error text; destructive buttons |

| Other token | Value |
|---|---|
| Fonts | `font-sans`: system UI fonts with Japanese fallbacks (Hiragino Sans, Noto Sans JP, Yu Gothic, Meiryo); `font-mono` for code. No web fonts (self-hosting and privacy). |
| Type scale | `text-caption` 12/16 · `text-body` 14/22 · `text-title` 15/22 · `text-heading` 20/28 · `text-display` 28/36 (px, size/line height) |
| Spacing | Tailwind's 4 px grid |
| Radius | `rounded-sm` 4 px · `rounded-md` 6 px · `rounded-lg` 10 px |
| Shadow | `shadow-card` (subtle lift) · `shadow-popover` (menus, dialogs) |
| Focus | 2 px `brand` outline, 2 px offset, on `:focus-visible` |

## Contrast (WCAG 2.2 AA)

Normal text, including button labels, links and typed text, needs 4.5:1;
large text — at least 24 px regular or 18.66 px bold (18 pt / 14 pt), or
the equivalent size for Japanese text — needs 3:1; non-text indicators that convey state (focus ring, input borders,
selection bar, online dot) 3:1 against what is next to them. Ratios are
truncated, never rounded up. Decorative `border` dividers carry no
information and are exempt; a control's boundary always uses
`border-strong`. The selected channel is shown by `on-selected` text **and** a
3 px `brand` bar, not by the `selected` fill alone.

| Pair (foreground on background) | Used for | Needed | Light | Dark |
|---|---|---|---|---|
| `fg` on `bg` | body text | 4.5:1 | 17.80:1 | 15.41:1 |
| `fg` on `sidebar` | sidebar text | 4.5:1 | 16.72:1 | 14.66:1 |
| `fg` on `input` | typed text | 4.5:1 | 16.72:1 | 14.66:1 |
| `muted` on `bg` | secondary text | 4.5:1 | 6.45:1 | 7.24:1 |
| `muted` on `sidebar` | secondary text in sidebar | 4.5:1 | 6.06:1 | 6.89:1 |
| `on-brand` on `brand` | button label | 4.5:1 | 5.14:1 | 9.74:1 |
| `on-selected` on `selected` | selected channel label | 4.5:1 | 8.07:1 | 11.82:1 |
| `on-accent` on `accent` | "New" divider label | 4.5:1 | 7.73:1 | 11.34:1 |
| `brand` on `bg` | link text | 4.5:1 | 5.14:1 | 10.09:1 |
| `brand` on `bg` | focus ring, selection bar | 3:1 | 5.14:1 | 10.09:1 |
| `brand` on `sidebar` | selection bar in sidebar | 3:1 | 4.83:1 | 9.60:1 |
| `brand` on `selected` | selection bar on selected row | 3:1 | 4.43:1 | 8.00:1 |
| `success` on `bg` | online dot | 3:1 | 5.35:1 | 9.30:1 |
| `success` on `sidebar` | online dot in sidebar | 3:1 | 5.03:1 | 8.85:1 |
| `warning` on `bg` | warning text | 4.5:1 | 6.32:1 | 10.18:1 |
| `warning` on `sidebar` | warning text in sidebar | 4.5:1 | 5.94:1 | 9.69:1 |
| `danger` on `bg` | error text | 4.5:1 | 6.57:1 | 8.26:1 |
| `danger` on `sidebar` | error text in sidebar | 4.5:1 | 6.17:1 | 7.86:1 |
| `on-danger` on `danger` | destructive button label | 4.5:1 | 6.57:1 | 8.47:1 |
| `border-strong` on `bg` | input border | 3:1 | 3.22:1 | 3.87:1 |
| `border-strong` on `input` | input border on input fill | 3:1 | 3.02:1 | 3.69:1 |

## Layout principles

- **Desktop:** workspace rail (68 px) · channel sidebar (≈270 px) · the
  conversation. Headers are thin; messages are a flat list with small
  avatars; the composer is pinned to the bottom.
- **Mobile:** one column — channel header, messages, composer, bottom
  navigation. Every tap target is at least 44 × 44 px.
- **Text first.** A message is readable before anything around it (avatars,
  images) has loaded.

## Planned directions (from the maintainer, #9)

Recorded now, designed when each screen is built:

- **Channels** use the flat chat layout above. **Direct messages** (after the
  MVP) use **speech bubbles**, so they feel like a conversation.
- Channel entries show an **image or photo** instead of `#`, and images are
  welcome elsewhere to make the app feel fun.
- **Performance comes first.** Images never delay messages: they are small
  and fixed-size with `width`/`height` set (no layout shift), lazy-loaded and
  decoded asynchronously, and cached as immutable; the text of a message
  renders immediately and images fill in after.
- **Playfulness** in design or motion is welcome — for example small,
  frog-like micro-animations — as long as it is cheap to render and turned
  off under `prefers-reduced-motion`.
