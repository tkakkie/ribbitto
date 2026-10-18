# Side-panel accessibility

Focus and modal rules for [markup and accessibility](accessibility.md).

A side panel (#874) is an `<aside>` labelled by its `h2`, which takes focus
when the panel opens (`autofocus` with `tabindex="-1"`). The open panel's
header control has `aria-current="true"`. Its close link, and Escape through
`side-panel-v3.js`, return to the conversation; Escape acts only with focus
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
