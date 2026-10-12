# Brand

ribbitto's logo and mascot, chosen by the maintainer on 2026-10-12 (#883).
How the rest of the UI looks is in [`ui.md`](ui.md).

**Keep it current:** update this file in the same pull request when a brand
file or one of these rules changes.

- **Logo:** the speech-bubble frog — a chat bubble with a tail, two eye
  bumps and three dots for a mouth (the dots #878 plans for typing). Files
  in `web/static/brand/`: `logo-v1.svg` (brand colour), `logo-mono-v1.svg`
  (`currentColor`) and `logo-tile-v1.svg` (white on brand, for app icons).
  Every page uses it as its favicon and `brandLogo` draws it; both read one
  constant, so replacing the logo changes one file and one reference. At
  least 16 px, with clear space of a quarter of its size; no other colours.
- **Mascot:** a flat frog without outlines in three poses —
  `mascot-hello-v1.svg` (welcome), `mascot-sleeping-v1.svg` (empty states)
  and `mascot-typing-v1.svg` (loading, sending). Its colours are the
  illustration colours listed in [`ui.md`](ui.md#tokens). It is decorative (`alt=""`) and never the only
  carrier of information.
- A changed brand file gets a new version suffix, since static files are
  cached as immutable.
