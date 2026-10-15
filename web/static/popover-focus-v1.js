(() => {
  // A popover is not modal, so Tab can move focus out of it while it stays
  // open over the control that now has focus (#1092). A Tab or Shift-Tab
  // that leaves focus outside an open popover closes it. Only the keyboard:
  // the browser already closes it on an outside click, and a click inside,
  // even on text that takes no focus, must keep it open.
  // Without :popover-open the selector would throw on every Tab.
  if (!CSS.supports?.("selector(:popover-open)")) return;
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Tab") return;
    const open = [...document.querySelectorAll(":popover-open")];
    if (open.length === 0) return;
    // Input such as Enter on a button can run before the timer. A popover it
    // opens or reopens belongs to that input, not to this Tab, so it stays.
    const opened = new Set();
    const note = (e) => { if (e.newState === "open") opened.add(e.target); };
    document.addEventListener("beforetoggle", note, true);
    // The browser moves focus after this handler. By then focus is outside,
    // so hiding does not return it to the button: it stays where Tab put it.
    setTimeout(() => {
      document.removeEventListener("beforetoggle", note, true);
      for (const popover of open) {
        if (opened.has(popover) || !popover.matches(":popover-open")) continue;
        if (!popover.contains(document.activeElement)) popover.hidePopover();
      }
    });
  });
})();
