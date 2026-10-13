# Brand

ribbitto's logo and mascot, chosen by the maintainer on 2026-10-12 (#883),
and the channel illustrations (#875).
How the rest of the UI looks is in [`ui.md`](ui.md).

**Keep it current:** update this file in the same pull request when a brand
file or one of these rules changes.

- **Logo:** the speech-bubble frog — a chat bubble with a tail, two eye
  bumps and three dots for a mouth (echoed by the typing dots). Files
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

## Channel illustrations

The repository's own SVG artwork in `web/static/channel-images-v1` is
covered by the repository licence. All twelve are square (120 × 120),
under 2 KB, and use only the pastel pairs in [`ui.md`](ui.md#tokens) and `bg` white:

| Pair | Keys / scenes |
|---|---|
| 1 | `lake`, `sail` |
| 2 | `sunrise`, `kite` |
| 3 | `hills`, `leaf`, `cactus` |
| 4 | `cup`, `shell` |
| 5 | `moon`, `mountain` |
| 6 | `flowers` |

To add one, draw a simple silhouette that reads at 24 px and 120 px,
save an SVG under 2 KB using these colours, and add its neutral key to
the registry in `internal/conversation/channel.go` and its asset test.
Use no external references, scripts or text. When changing artwork,
create a new versioned directory and update both the embed declaration
and `view.ChannelImageURL`; never change a file at an immutable URL. Keep registry
order stable: NULL and unknown keys select by SHA-256 of the channel ID,
modulo the set size. Adding a key may change these fallback assignments;
stored known keys keep their image. The welcome card shows four of them (`welcomeImages`). Members choose from the same registry in the create form and overview
panel (#876); the overview shows the image beside the name at 32 px.
