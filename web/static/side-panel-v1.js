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
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape" || event.defaultPrevented || event.isComposing) return;
    // Escape belongs to an open popover, and never discards a draft: only
    // focus in the panel, or on nothing, closes it.
    const active = document.activeElement;
    if (document.querySelector(":popover-open")) return;
    if (active !== document.body && !document.getElementById("side-panel").contains(active)) return;
    remember();
    location.assign(close.href);
  });
})();
