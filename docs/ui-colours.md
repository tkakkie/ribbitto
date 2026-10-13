# UI colours

The colour tokens, their light and dark values, and the contrast they
meet. The direction behind them is in [`ui.md`](ui.md#direction).

**Keep it current:** update this file in the same pull request whenever a
colour token or the contrast table changes. The tokens themselves live
only in `web/styles/app.css`; this file explains them.

## Colour tokens

Tailwind's default colour palette is switched off, so only
these colours exist in templates; `make check` rejects literal colours
([`ui-code.md`](ui-code.md#styles)). The dark values are chosen (#966);
#967 applies them, and until then every page stays light.

| Colour token | Light | Dark | Use |
|---|---|---|---|
| `base` | `#edf4f2` | `#0f1514` | page ground; the sidebar sits on it |
| `bg` | `#ffffff` | `#1a2120` | blocks: the conversation, panels |
| `rail` | `#ffffff` | `#1a2120` | the app bar block |
| `sidebar` | `#edf4f2` | `#0f1514` | reserved for the sidebar (it sits on `base`) |
| `panel` | `#f5f9f8` | `#161d1c` | reserved for side panels (they use `bg`) |
| `surface` | `#ffffff` | `#212928` | cards, popovers |
| `input` | `#ffffff` | `#212928` | text fields |
| `hover` | `#e4edeb` | `#26302e` | hovered rows and buttons |
| `border` | `#e1e8e6` | `#2b3432` | decorative dividers only |
| `border-strong` | `#7f8986` | `#768380` | borders that identify a control (inputs) |
| `fg` | `#171c1b` | `#e4ecea` | body text |
| `muted` | `#545d5b` | `#9ba7a4` | secondary text, timestamps |
| `brand` / `on-brand` | `#0f766e` / `#ffffff` | `#4cc9b8` / `#062421` | primary buttons, focus ring, selection indicator, unread counts, links |
| `selected` / `on-selected` | `#d5f1ea` / `#0b4d48` | `#1d3d38` / `#b8ece2` | the current row |
| `accent` / `on-accent` | `#fff0e6` / `#8a3a12` | `#3a2216` / `#ffc6a3` | the "New" divider's label |
| `accent-line` | `#f2a37b` | `#a8562f` | the "New" divider's line (decorative; the label carries the meaning) |
| `success` | `#3a9742` | `#5bc46a` | online dot (always with a label) |
| `warning` | `#8a5300` | `#e9b55c` | reserved for warning text and icons (e.g. "reconnecting…"); none yet |
| `danger` / `on-danger` | `#b42318` / `#ffffff` | `#ff8b7b` / `#2b0a05` | error text / reserved for destructive buttons (none yet) |
| `pastel-1`…`6` / `on-pastel-1`…`6` | `#dcebff`/`#1f5aa6`, `#fff3c4`/`#7a5d00`, `#d9f2e6`/`#1d6b47`, `#ffe4d6`/`#9a4a1f`, `#ebe3ff`/`#5b3fa8`, `#ffe0ea`/`#a3284f` | `#1c3047`/`#a9ccff`, `#3a3214`/`#f2d77a`, `#173628`/`#9fdcbc`, `#3d2618`/`#ffbe98`, `#2b2347`/`#c9b8ff`, `#3d1a26`/`#ffaec6` | channel images and avatar fills / their initials |

A deep-teal dark brand (white text on `#0f766e`) was rejected: its fill
is 2.99:1 against the dark blocks, under 3:1 for a control.

**Illustration colours** belong to the brand artwork only
([`brand.md`](brand.md)), never to CSS: the mascot's body `#4cc9a0`, shade
`#2f9e83`, belly `#e9fbf3`, eyes `#1b2a2f`, cheeks `#ff9fb0` and mouth
`#b2384f`, the lily pad `#6cc59a` and sleep marks `#8a9a95`, with white,
black shadows and the `selected` and `brand` tokens.

## Contrast (WCAG 2.2 AA)

Normal text, including button labels, links and typed text, needs 4.5:1;
large text — at least 24 px regular or 18.66 px bold (18 pt / 14 pt), or
the equivalent size for Japanese text — needs 3:1; non-text indicators that convey state (focus ring, input borders,
selection indicator, online dot) 3:1 against what is next to them. Ratios are
truncated, never rounded up. Decorative `border` and `accent-line` carry no
information and are exempt; a control's boundary always uses
`border-strong`. The current row is shown by `on-selected` text **and** the
`brand` indicator, not by the `selected` fill alone. In light `bg`, `rail`,
`surface` and `input` share one value, and `sidebar` equals `base`, so each
row below covers them. The table lists light ratios; in dark `surface` and
`input` are lighter than `bg`, so #967 adds the dark pairs with their own
rows for them (text, placeholder, control borders and the focus outline).

| Pair (foreground on background) | Used for | Needed | Ratio |
|---|---|---|---|
| `fg` on `bg` / `base` / `panel` / `hover` | body text (`panel` reserved) | 4.5:1 | 17.23 / 15.44 / 16.24 / 14.45 |
| `muted` on `bg` / `base` / `panel` / `hover` | secondary text (`panel` reserved) | 4.5:1 | 6.78 / 6.08 / 6.39 / 5.69 |
| `on-brand` on `brand` | button label, unread count | 4.5:1 | 5.47 |
| `on-selected` on `selected` | current row's label | 4.5:1 | 8.11 |
| `on-accent` on `accent` | "New" divider label | 4.5:1 | 6.99 |
| `brand` on `bg` / `base` | link text; focus ring | 4.5:1 | 5.47 / 4.90 |
| `brand` on `selected` | indicator on the current row | 3:1 | 4.58 |
| `success` on `bg` / `base` | online dot | 3:1 | 3.69 / 3.31 |
| `warning` on `bg` / `base` | warning text (reserved) | 4.5:1 | 6.32 / 5.67 |
| `danger` on `bg` / `base` | error text | 4.5:1 | 6.57 / 5.89 |
| `on-danger` on `danger` | destructive button label (reserved) | 4.5:1 | 6.57 |
| `border-strong` on `bg` / `base` | input border | 3:1 | 3.60 / 3.23 |
| `on-pastel-n` on `pastel-n` (1…6) | initials on avatars | 4.5:1 | 5.65 / 5.56 / 5.48 / 5.13 / 6.23 / 5.75 |
