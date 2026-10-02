# Markup and accessibility

The target is WCAG 2.2 AA. `TestPagesMarkup` (`internal/web/markup_test.go`)
renders every HTML route in each state and both languages and fails on the
rules marked ✓; a new route fails the test until it has a rendered case.

- **Things that act are buttons or links.** ✓ A `<button>` always has a
  `type` and a name (its text, or `aria-label` when it has none); a link is
  an `<a href>`. Never a `div` or `span` with `role="button"` or a click
  handler.
- **Pages are titled.** ✓ Every page's `<title>` names the page first and
  the app last, joined by ` · `: a localised purpose (`Sign in · ribbitto`),
  or the channel and organisation (`general · Acme · ribbitto`). The app
  name alone fails the check.
- **Structure means something.** ✓ Every page has `lang` and exactly one
  `<main>`; ✓ headings do not skip levels. Use `<nav>`, `<header>` and lists
  (`<ul>`, `<ol>`) for what they are; `div` and `span` only group for
  layout.
- **Forms are labelled.** ✓ Every text input, `<select>` and `<textarea>` has
  a label — a wrapping `<label>` or `for`/`id`; hidden inputs need none.
  Field labels contain only the field name; errors sit outside the label,
  linked from the input with `aria-describedby`, and use `role="alert"`
  after a submit. Setup and sign-up use the shared `textField` component:
  ✓ invalid inputs have `aria-invalid="true"` and an error description;
  valid inputs omit both attributes. Sign-in keeps one form-level alert.
- **References resolve.** ✓ IDs are unique; `for`, `aria-describedby` and
  `aria-labelledby` targets exist in the rendered document or fragment.
- **Timestamps.** ✓ `<time datetime>` values are valid HTML global dates
  and times, including a timezone and at most three fractional digits.
- **Images** ✓ have `alt` (empty when decorative).
- **Focus.** ✓ `tabindex` only as `-1` on a deliberate focus target (a
  dialog's heading, an error summary) or `0` on a scrollable region with
  `role="region"` and an `aria-label`. Focus is always visible (the focus
  token); nothing removes the outline.
- **Colour is never the only signal** (see [*Direction*](ui.md#direction)): a state also has
  text, an icon or a shape.
- **Styles** ✓ only through the token utilities: no `style` attributes and
  no global CSS beyond `web/styles/app.css`.
- **Scripts.** ✓ No inline handlers (`on*` attributes); application-written
  JavaScript is external, in `web/static`, and defines no globals. Vendored
  libraries (htmx, idiomorph) keep theirs. The nonce and external-script
  rules are in [`architecture/rendering.md`](architecture/rendering.md).
- **Assets exist.** ✓ Every `/static/` script and stylesheet URL resolves
  to a file in the embedded assets, using its path without the query string.

**Checking a UI change in a browser**, before the pull request says what
was checked: use it with the keyboard only (Tab order, Enter and Space,
Escape closes what opened); watch that focus is always visible; and look at
it under a colour-vision simulation (Chrome DevTools, *Rendering → Emulate
vision deficiencies*) to see that no information is lost. The automated
checks find only part of accessibility problems, so these stay necessary.
They verify reference targets exist, not that they describe the right field;
they do not execute JavaScript or check `hx-*` behaviour.

The `<ol>` isolates authors, timestamps and plain-text bodies
(`dir="auto"`, preserved line breaks). `hidden peer-empty:flex` hides its empty
state when populated. Its labelled, keyboard-focusable pane scrolls independently.
The empty `#message-status` has `role="status"`, `aria-live="polite"` and
`aria-relevant="additions"`. History and paging stay outside live regions.
Only live appends announce stream-only text. History, prepends and duplicate
replacements stay silent. The status keeps the latest 10 entries; pruning is silent.
The textarea keeps invalid drafts and links errors with `aria-describedby`;
htmx submissions return focus to it. "Load older messages"
is a plain link without JavaScript; after a prepend, the reading position
stays put; focus moves to the next link, or the pane at the start.
