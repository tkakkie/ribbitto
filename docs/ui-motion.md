# UI motion

Motion tokens and interaction rules for the [UI code](ui-code.md).

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

