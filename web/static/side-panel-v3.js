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
  const backdrop = document.getElementById("panel-backdrop");
  backdrop.addEventListener("click", remember);
  // Below the wide breakpoint the panel covers the conversation (out of
  // flow: an overlay, or a phone's sheet) and is a modal dialog: everything
  // else on the page is inert, Tab stays inside, and widening the window
  // releases it to the complementary panel beside the conversation.
  // Only this script decides it, and aria-modal publishes it.
  const panel = document.getElementById("side-panel");
  const background = [];
  for (let node = panel; node.parentElement && node !== document.body; node = node.parentElement) {
    for (const sibling of node.parentElement.children) if (sibling !== node && sibling !== backdrop) background.push(sibling);
  }
  let modal = false;
  const publish = () => {
    modal = getComputedStyle(panel).position !== "static";
    for (const element of background) element.inert = modal;
    if (modal) {
      panel.setAttribute("role", "dialog");
      panel.setAttribute("aria-modal", "true");
    } else {
      panel.removeAttribute("role");
      panel.removeAttribute("aria-modal");
    }
  };
  const cover = () => {
    publish();
    // Narrowing the window would otherwise leave focus behind the panel.
    if (modal && !panel.contains(document.activeElement)) document.getElementById("panel-heading").focus();
  };
  cover();
  window.addEventListener("resize", cover);
  // read-visibility-v6.js dispatches panel-cover on the panel just before it
  // reads aria-modal, so it never reads a stale state: a page shown again
  // after a size change while hidden gets its resize event only later.
  panel.addEventListener("panel-cover", publish);
  // Tab and Shift-Tab wrap inside the dialog rather than leave the page.
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Tab" || !modal) return;
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
