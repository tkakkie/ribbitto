# Markup and accessibility

Target: WCAG 2.2 AA. `TestPagesMarkup` (`internal/web/markup_test.go`) renders
every HTML route/state in both languages, enforcing ✓ rules; new routes
fail until covered by a rendered case.

- **Things that act are buttons or links.** ✓ A `<button>` always has a
  `type` (#1500) and a name (its text, or `aria-label` when it has none); a link is
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
- **Forms and tap targets:** see [`accessibility-forms-and-targets.md`](accessibility-forms-and-targets.md).
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
- **Colour is never the only signal** (see [*Direction*](ui.md#direction)): a state also has
  text, an icon or a shape.
- **Styles** ✓ only through the token utilities: no `style` attributes and
  no global CSS beyond `web/styles/app.css`.
- **Scripts.** ✓ No inline handlers (`on*` attributes); application-written
  JavaScript is external, in `web/static`, and defines no globals. Vendored
  libraries (htmx, idiomorph) keep theirs. The nonce and external-script
  rules are in [`architecture/csp.md`](architecture/csp.md).
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
returns to it. Pending sends queue ([contract](architecture/web-composer-pending.md)).
A polite `role="status"` shows "Sending…" over the help, which turns invisible but keeps its space. "Load older"
is a plain link without JavaScript; after a prepend, the reading position
stays put; focus moves to the next link, or the pane at the start.

The account menu or sheet (`#account-menu`) and the create popover
(`#channel-create`) and a message's actions menu (`message-actions-<hex>`,
#1159) are non-modal native popovers. Tab/Shift-Tab leaving closes it
and keeps focus on the reached control (#1092) (`web/src/popover-focus.ts`, on every page;
it suits all three, which nest no popover). They close on Tab exit because outside clicks already close them. Escape closes it and focuses its button; outside clicks close it, keeping
clicked focus; inside clicks keep it open except Quote. Without JavaScript, Tab can still move behind an open
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
The close link and `web/src/side-panel.ts`'s Escape return to the conversation.
Escape acts only with focus in the panel or on nothing and no popover open;
Escape in the composer keeps the panel. Closing focuses the opener, whose id
crosses the load in `sessionStorage`. Session-bound device drafts return after
panel navigation without input, typing or focus changes.

Below 1200 px the panel covers the conversation as a modal dialog, sharing
the overlay/phone sheet model (#972, [side panels](ui-panels-and-motion.md#side-panels)).
The script sets `role="dialog"`, `aria-modal="true"` and makes the rest of the
page inert (app bar, sidebar or list, conversation), blocking focus and pointers;
focus left behind moves to the heading. Tab from the last stop, and Shift-Tab
from the first stop or heading, wrap inside. Widening to 1200 px removes
`role`, `aria-modal` and `inert`, restoring the complementary `<aside>`;
crossing 852 px either way keeps the modal. The dimmed `#panel-backdrop` covers the page
as a pointer-only close link (`tabindex="-1"`, `aria-hidden="true"`); the close
link and Escape serve keyboard and assistive technology. All three return
focus to the opener. Without JavaScript the panel is a page with working
close link and member paging. `testPanelSheetBrowser` checks the accessibility
tree at both widths, sheet height, scrolling and reading pause.
