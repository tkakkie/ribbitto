(() => {
  // Closing a panel is a page load, so the opener's id crosses it in
  // sessionStorage: a focus hint whose loss only leaves focus at the top.
  const key = "panel-opener";
  let opener = null;
  try {
    opener = sessionStorage.getItem(key);
    sessionStorage.removeItem(key);
  } catch {
    // Storage is off; closing still works.
  }
  const close = document.getElementById("panel-close");
  if (!close) {
    if (opener) document.getElementById(opener)?.focus();
    return;
  }
  const remember = () => {
    const control = document.querySelector("[id^='panel-control-'][aria-current]");
    try {
      if (control) sessionStorage.setItem(key, control.id);
    } catch {
      // As above.
    }
  };
  close.addEventListener("click", remember);
  document.getElementById("panel-backdrop").addEventListener("click", remember);
  // While the panel covers the conversation (out of flow below the wide
  // breakpoint), the conversation is inert: focus and pointer stay out of it,
  // and widening the window restores it.
  const panel = document.getElementById("side-panel");
  const conversation = document.getElementById("conversation");
  const cover = () => {
    const covered = getComputedStyle(panel).position !== "static";
    // Narrowing the window would otherwise drop focus from the composer.
    if (covered && conversation.contains(document.activeElement)) document.getElementById("panel-heading").focus();
    conversation.inert = covered;
  };
  cover();
  window.addEventListener("resize", cover);
  // A phone's sheet is all there is to reach: Tab and Shift-Tab wrap
  // inside it rather than leave the page.
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Tab" || getComputedStyle(panel).position !== "fixed") return;
    const stops = [...panel.querySelectorAll("a[href], button, summary, input, select, textarea")].filter((e) => e.tabIndex >= 0 && !e.disabled && e.checkVisibility());
    if (stops.length === 0) return;
    const [first, last] = [stops[0], stops.at(-1)];
    const active = document.activeElement;
    // From the heading, which takes focus on opening, Shift-Tab wraps too.
    const outside = active.id === "panel-heading" || !panel.contains(active);
    if (!outside && active !== (event.shiftKey ? first : last)) return;
    event.preventDefault();
    (event.shiftKey ? last : first).focus();
  });
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape" || event.defaultPrevented || event.isComposing) return;
    // Escape belongs to an open popover, and Escape in the composer never
    // closes the panel: only focus in the panel, or on nothing, closes it.
    const active = document.activeElement;
    if (document.querySelector(":popover-open")) return;
    if (active !== document.body && !panel.contains(active)) return;
    remember();
    location.assign(close.href);
  });
})();
