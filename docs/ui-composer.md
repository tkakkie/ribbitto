# Composer growth

The textarea uses `field-sizing-content`, with no animation. Empty, it is
38 px from `desk` (one 22 px line and 16 px padding), and at least 44 px
below `desk` for the phone tap target. Typed, pasted, composing and preset
text all grow it; successful sends clear only the sent text.

`max-h-composer` caps it at **max(three lines, min(half the visible viewport,
the height leaving the list 128 px))**. Three lines are 82 px including
padding and always win when space is short. The cap subtracts the column
budget (225 px from `desk`, 209 px below, including label/buttons), 128 px,
and `--composer-quote-h` (measured chip plus flex gap). Load, chip swaps and
composer resizes refresh that deduction. Below `desk` it also subtracts
`env(safe-area-inset-bottom)` to reserve the composer padding conservatively,
even with a keyboard. Past the cap the textarea scrolls inside.
After input and textarea resizing, the script scrolls a focused,
collapsed caret at the end to the bottom using the updated layout; other
caret positions and selections keep native scrolling. Below `short:` the cap
is three lines and the conversation still scrolls as a whole
([short heights](ui-components.md#short-heights)).

The script sets `--composer-viewport-h` / `--composer-viewport-top` on
`#organization-stream`, which carries `h-conversation-viewport` and contains
the textarea. It writes only when `visualViewport.height` differs from
`innerHeight` or `offsetTop` from zero by more than 1 px; matching again removes
both overrides. Normal desktop loads and browsers without that API use the
CSS `100dvh` fallback. Viewport resize/scroll and window resize/orientation
listeners write only changed values. iOS Safari's keyboard shrinks the visual
viewport but not `dvh`; height plus `offsetTop` keeps the frame's bottom at the
visible bottom when Safari pans. The frame is outside every htmx swap target;
navigation uses full pages, so no htmx history restore replaces it.

First layout measurements wait for the list/textarea/form ResizeObserver,
before paint. It scrolls to newest (or keeps the native message fragment),
remembers that anchor, then measures/writes the quote deduction last. CSS's
zero fallback needs no write without a chip. If the deduction resizes the
composer, another observer delivery restores the anchor before paint without
forcing layout after that first write.

The composer script remembers whether `#message-list` is at its newest
message (gap at most 2 px), or its first visible item and offset. On a list
resize it keeps that place, including quote, keyboard or window resizing; shrinking
can clamp the position when there is too little content below it. Scroll
events at a changed height cannot replace the saved position before restoration.
Chip measurements hold that anchor even if the final list height is unchanged;
native resize scrolling does not cancel it. One resize observer restores the
list before revealing the end caret. After htmx settles, a microtask records
history/stream scrolling, including the stream's successful-post scroll, so
it takes precedence over resizing. Checking a message keeps the grown
composer's space behind the move form.

Without `field-sizing` support, `rows="3"` keeps the old 82 px box with
inner scrolling; the 44 px phone minimum still applies. No sizing script
fallback is added. `testComposerGrowthBrowser` covers growth and scrolling;
phone IME send guards remain in the phone conversation browser test.
