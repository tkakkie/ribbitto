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
  or the channel and organisation (`general · Acme · ribbitto`); the channel
  list page names the list and the organisation (`lilypads · Acme · ribbitto`). The app
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
  valid inputs omit both attributes. Sign-in uses `textField` with empty
  field errors and keeps one form-level alert. A radio group's error (the
  channel image picker) is its own alert: every radio in the group has
  `aria-invalid="true"` and is described by it.
- **References resolve.** ✓ IDs are unique; `for`, `aria-describedby` and
  `aria-labelledby` targets exist in the rendered document or fragment, and
  `form` targets in the page.
- **Timestamps.** ✓ `<time datetime>` values are valid HTML global dates
  and times, including a timezone and at most three fractional digits.
- **Images** ✓ have `alt` (empty when decorative).
- **Focus.** ✓ `tabindex` only as `-1` on a deliberate focus target (a
  dialog's heading, an error summary) or `0` on a scrollable region with
  `role="region"` and an `aria-label`. Focus is always visible (the focus
  token); nothing removes the outline.
- **Tap targets** at phone width are at least 44 × 44 px, stricter than
  AA's 24 px (2.5.8), which desktop keeps: links, buttons, form controls,
  `<summary>` and labels that act as a control's target. Links inside
  running text (message and help text) are the one exception
  ([`ui.md`](ui.md#layout-principles), #966). Welcome text fields and
  buttons have a 48 px minimum height below `desk`, with at least 16 px
  field text (#973). `TestBrowser/width-audit`
  (`internal/web/width_audit_browser_test.go`) checks these targets, control
  reachability, and document and scroll-container horizontal overflow on
  every page at phone, landscape and breakpoint widths (#974). A target
  label is a checkbox's or radio's label, which stands in for the small
  native widget; a text field's caption is not, since the field itself is
  the target.
- **Colour is never the only signal** (see [*Direction*](ui.md#direction)): a state also has
  text, an icon or a shape.
- **Styles** ✓ only through the token utilities: no `style` attributes and
  no global CSS beyond `web/styles/app.css`.
- **Scripts.** ✓ No inline handlers (`on*` attributes); application-written
  JavaScript is external, in `web/static`, and defines no globals. Vendored
  libraries (htmx, idiomorph) keep theirs. The nonce and external-script
  rules are in [`architecture/rendering.md`](architecture/rendering.md).
- **Icons are decorative.** The shared `Icon` component hides its SVG from
  assistive technology and keyboard focus; icon-only buttons need `aria-label`.
- **Assets exist.** ✓ Every `/static/` script, stylesheet, icon link, image
  and SVG `<use>` URL resolves to an embedded file, ignoring queries and
  fragments. SVG fragments
  must name a symbol in that sprite. `TestTemplateIcons` also checks every
  literal `Icon` call in templates, including ones outside the page fixtures.

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
Latest feeds/topics have a separate visible `#typing-status`, localized and
polite with `aria-atomic="true"`. Content replacement keeps the region and focus;
it stays outside history and composer replacement, with no keyboard stop.
The textarea keeps invalid drafts and links errors with `aria-describedby`;
htmx submissions return focus to it. "Load older"
is a plain link without JavaScript; after a prepend, the reading position
stays put; focus moves to the next link, or the pane at the start.

The move form (`#branch-form`) shows only while a message is checked (#923),
through CSS `:has()` alone, so it behaves the same without JavaScript. Until
then it is `display: none`: out of the tab order and the accessibility tree.
Checking a message never moves focus; the form appears next in the tab order
after the list, over the composer's area, which turns invisible but keeps its
space, so the list neither resizes nor scrolls. A plain failed submit, which
checks nothing, keeps the form and its alert in the column above the composer;
a browser without `:has()` shows both, as before.

A side panel (#874) is an `<aside>` labelled by its `h2`, which takes focus
when the panel opens (`autofocus` with `tabindex="-1"`). The open panel's
header control has `aria-current="true"`. Its close link, and Escape through
`side-panel-v2.js`, return to the conversation; Escape acts only with focus
in the panel or on nothing and no popover open, so Escape in the composer
never closes the panel (a draft is still lost by leaving the page). After that close, focus returns to the control that opened the panel:
its id crosses the page load in `sessionStorage`.

Below 1200 px the panel covers the conversation and is a modal dialog,
one model for the overlay and the phone's sheet (#972,
[side panels](ui-components.md#side-panels)). The script gives it
`role="dialog"` and `aria-modal="true"` and makes everything else on the
page inert (the app bar, the sidebar or list, and the conversation), so
neither focus nor a pointer reaches them; focus left behind moves to the
heading. Tab from the last stop, and Shift-Tab from the first stop or the
heading, wrap inside the dialog. Widening the window to 1200 px removes the
role, `aria-modal` and `inert`, leaving the complementary `<aside>`;
crossing 852 px either way keeps the modal. The dimmed `#panel-backdrop`,
over the whole page, is a close link for a pointer only (`tabindex="-1"`,
`aria-hidden="true"`); the close link and Escape serve the keyboard and
assistive technology, and focus returns to the opener after any of the
three. The modal semantics come with the script: without JavaScript the
panel is a page whose close link and member paging work.
`testPanelSheetBrowser` checks this through the accessibility tree at both
widths, with the sheet's height, scrolling and the reading pause.
