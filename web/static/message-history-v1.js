(() => {
  // Older messages are prepended above the ones being read. Keep the
  // distance from the bottom of the pane so the reader stays in place.
  let fromBottom = null;
  document.addEventListener("htmx:beforeSwap", (event) => {
    if (event.detail.target.id !== "message-items") return;
    const pane = document.getElementById("message-list");
    fromBottom = pane.scrollHeight - pane.scrollTop;
  });
  document.addEventListener("htmx:afterSwap", (event) => {
    if (event.detail.target.id !== "message-items" || fromBottom === null) return;
    const pane = document.getElementById("message-list");
    pane.scrollTop = pane.scrollHeight - fromBottom;
    fromBottom = null;
    // The activated control was replaced; give focus to the next one, or to
    // the pane once the oldest message is shown.
    const next = document.querySelector("#load-older a") || pane;
    next.focus({ preventScroll: true });
  });
})();
