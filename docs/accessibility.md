# Markup and accessibility

The target is WCAG 2.2 AA. `TestPagesMarkup` (`internal/web/markup_test.go`)
renders every HTML route in each state and both languages and fails on the
rules marked ✓; a new route fails the test until it has a rendered case.

- **Things that act are buttons or links.** ✓ A `<button>` always has a
  `type` and a name (its text, or `aria-label` when it has none); a link is
  an `<a href>`. Never a `div` or `span` with `role="button"` or a click
  handler. Listbox options are the exception (the composer's @-picker): the
  textarea keeps focus, `aria-activedescendant` names the option, and options
  are at least 44 px tall below `desk` ([contract](architecture/web-composer.md#composer)).
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
  a `<label>` (wrapping or `for`/`id`); hidden inputs need none.
  Field labels contain only the field name; errors sit outside the label,
  linked from the input with `aria-describedby`, and use `role="alert"`
  after a submit. Setup and sign-up use the shared `textField` component:
  ✓ invalid inputs have `aria-invalid="true"` and an error description;
  valid inputs omit both attributes. Sign-in uses `textField` with empty
  field errors and keeps one form-level alert. A radio group's error (the
  channel image picker) is its own alert: every radio in the group has
  `aria-invalid="true"` and is described by it. Enhanced failures fill
  `#message-send-alert` (`role="alert"`, outside swaps) with localised
  not-sent, unconfirmed or recovered text. Pending failures
  use an empty polite status; queued/sending states are silent.
  Retry/Edit/Remove describe their unique body. With composer text, Edit is
  disabled; `aria-describedby` also names its visible localised explanation.
  Edit focuses the restored text's end. At 10 items,
  a localised capacity alert retains the draft, `aria-invalid` and
  `aria-describedby`.
- **References resolve.** ✓ IDs are unique; `for`, `aria-describedby` and
  `aria-labelledby` targets exist in the rendered document or fragment, and
  `form` targets in the page.
- **Timestamps.** ✓ `<time datetime>` values are valid HTML global dates
  and times, including a timezone and at most three fractional digits.
- **Images** ✓ have `alt` (empty when decorative).
- **Focus.** ✓ `tabindex` only as `-1` on a deliberate focus target (a
  dialog's heading, an error summary, the account menu's first line) or
  `0` on a scrollable region with `role="region"` and an `aria-label`.
  Focus is always visible (the focus token); nothing removes the outline.
- **Tap targets** at phone width are at least 44 × 44 px, stricter than
  AA's 24 px (2.5.8), which desktop keeps: links, buttons, form controls,
  `<summary>` and labels that act as a control's target. The exceptions
  are links inside running text (message and help text) and a control's
  caption label while the control itself is a 44 × 44 px target
  ([`ui.md`](ui.md#layout-principles), #966, #974). Welcome text fields and
  buttons have a 48 px minimum height below `desk`, and every text input,
  textarea and select has at least 16 px text there (#973, #1120). `TestBrowser/width-audit`
  (`internal/web/width_audit_browser_test.go`) checks these targets, control
  reachability, and document and scroll-container horizontal overflow on
  every page at phone, landscape and breakpoint widths (#974). A target
  label is a checkbox's or radio's label, which stands in for the small
  native widget; another control's caption is exempt only while that
  control is itself a 44 × 44 px target.
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
was checked: use only the keyboard (Tab order, Enter, Space, Escape closes
what opened); check visible focus and a colour-vision simulation (Chrome
DevTools, *Rendering → Emulate vision deficiencies*) to see that no information
is lost. Automated checks verify reference targets exist, not that they describe
the right field, and run neither JavaScript nor `hx-*`.

The `<ol>` isolates authors, timestamps and plain-text bodies
(`dir="auto"`, preserved line breaks). `hidden peer-empty:flex` hides its empty
state when populated. Its labelled, keyboard-focusable pane scrolls independently.
`#message-status` is empty, with `role="status"`, `aria-live="polite"` and
`aria-relevant="additions"`. It announces only live appends' stream-only text and
"Copy link" confirmation (#1159), keeping 10 entries. History, paging,
prepends, duplicate replacements and pruning stay silent.
A reset hold shows `#stream-stopped`, a localised status with a plain reload link, keeping focus.
Latest feeds/topics' `#typing-status` is visible, localised and polite with
`aria-atomic="true"`, outside history and composer swaps. Content swaps keep
region and focus, without a tab stop.
The textarea is never replaced (#1173): it keeps drafts and text typed while a
post is in flight, keeps `aria-describedby` and takes
`aria-invalid` from every swapped state (`"false"` on pending success).
Help shows errors or guidance; focus left on the disabled send button
returns to it. Pending sends queue ([contract](architecture/web-composer.md#composer)).
A polite `role="status"` shows "Sending…" over the help, which turns invisible but keeps its space. "Load older"
is a plain link without JavaScript; after a prepend, the reading position
stays put; focus moves to the next link, or the pane at the start.

The move form (`#branch-form`) shows only while a message is checked (#923),
through CSS `:has()` alone, so it behaves the same without JavaScript. Until
then it is `display: none`: out of the tab order and the accessibility tree.
Checking a message never moves focus; the form appears next in the tab order
after the list, over the composer's area, which turns invisible but keeps its
space, so the list neither resizes nor scrolls. A plain failed submit, which
checks nothing, keeps the form and its alert in the column above the composer;
a browser without `:has()` shows both, as before. Below the
[short height](ui-components.md#short-heights) the form follows the list in
the column instead, and the composer is hidden.

The account menu or sheet (`#account-menu`) and the create popover
(`#channel-create`) and a message's actions menu (`message-actions-<hex>`,
#1159) are native popovers, which are not modal, so Tab can
leave them while they stay open. Once a Tab or Shift-Tab settles, focus
does not rest outside an open popover, where the popover could cover it
(#1092): a Tab or Shift-Tab that moves focus out of it closes it, and focus
stays on the control Tab moved to (`popover-focus-v1.js`, on every page;
it suits all three, which nest no popover). They close rather than keep Tab
inside because an outside click already closes them. Escape closes one
with focus back on its button; an outside click closes it and leaves focus
where the click put it; a click inside leaves it open except for Quote. Without JavaScript, Tab can still move behind an open
popover. `testPopoverFocusBrowser` checks each at desktop and
phone width.

Opening the account menu, by keyboard or pointer, moves focus to its first
line, the signed-in text (`tabindex="-1"` and `autofocus`, the browser's
own popover focusing, so also without JavaScript), because below `desk` the
sheet covers the bottom navigation's button that opened it (#1102). Text
rather than sign-out, so a repeated Enter or Space acts on nothing and the
menu stays open; Tab then reaches sign-out. The create popover keeps focus on its button. `testPopoverFocusBrowser` checks opening with
Enter and with Space at desktop and phone width.

A side panel (#874) is an `<aside>` labelled by its `h2`, focused on opening
(`autofocus` with `tabindex="-1"`). Its header control has `aria-current="true"`.
The close link and `side-panel-v3.js`'s Escape return to the conversation.
Escape acts only with focus in the panel or on nothing and no popover open;
Escape in the composer keeps the panel. Closing focuses the opener, whose id
crosses the load in `sessionStorage`. Session-bound device drafts return after
panel navigation without input, typing or focus changes.

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
