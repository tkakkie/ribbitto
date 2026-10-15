(() => {
  // A popover is not modal, so Tab can move focus out of it while it stays
  // open over the control that now has focus (#1092). A Tab or Shift-Tab
  // that leaves focus outside an open popover closes it. Only the keyboard:
  // the browser already closes it on an outside click, and a click inside,
  // even on text that takes no focus, must keep it open.
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Tab") return;
    // The browser moves focus after this handler. By then focus is outside,
    // so hiding does not return it to the button: it stays where Tab put it.
    setTimeout(() => {
      for (const popover of document.querySelectorAll(":popover-open")) {
        if (!popover.contains(document.activeElement)) popover.hidePopover();
      }
    });
  });
})();
