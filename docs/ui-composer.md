# Composer growth

The textarea uses `field-sizing-content`, with no animation. Empty, it is
38 px from `desk` (one 22 px line and 16 px padding), and at least 44 px
below `desk` for the phone tap target. Typed, pasted, composing and preset
text all grow it; successful sends clear only the sent text.

`max-h-composer` caps it at **max(three lines, min(half the viewport,
the height leaving the list 128 px))**. Three lines are 82 px including
padding and always win when space is short. Without a quote chip, the column outside the
textarea and list is 225 px from `desk`, 209 px below it, rounded up;
the cap subtracts those plus 128 px (353 / 337 px). Below `desk` it also
subtracts `env(safe-area-inset-bottom)`. Past the cap the textarea scrolls
inside. A quote chip uses extra column space without changing the cap.
After input and textarea resizing, the script scrolls a focused,
collapsed caret at the end to the bottom using the updated layout; other
caret positions and selections keep native scrolling. Below `short:` the cap
is three lines and the conversation still scrolls as a whole
([short heights](ui-components.md#short-heights)).

The composer script remembers whether `#message-list` is at its newest
message (gap at most 2 px), or its first visible item and offset. On a list
resize it keeps that place, including keyboard or window resizing; shrinking
can clamp the position when there is too little content below it. Scroll
events at a changed height cannot replace the saved position before restoration;
native resize scrolling does not cancel it. One resize observer restores the
list before revealing the end caret. After htmx settles, a microtask records
history/stream scrolling, including the stream's successful-post scroll, so
it takes precedence over resizing. Checking a message keeps the grown
composer's space behind the move form.

Without `field-sizing` support, `rows="3"` keeps the old 82 px box with
inner scrolling; the 44 px phone minimum still applies. No sizing script
fallback is added. `testComposerGrowthBrowser` covers growth and scrolling;
phone IME send guards remain in the phone conversation browser test.
