(() => {
  // Older messages are prepended above the ones being read. Keep the
  // distance from the bottom of the pane so the reader stays in place.
  let fromBottom = null;
  const pane = () => document.getElementById("message-list");
  const restore = () => {
    pane().scrollTop = pane().scrollHeight - fromBottom;
  };
  const olderPage = (event) => event.detail.target?.id === "message-items";
  document.addEventListener("htmx:beforeSwap", (event) => {
    if (olderPage(event)) fromBottom = pane().scrollHeight - pane().scrollTop;
  });
  // Restore once on swap so the page does not jump, and again after
  // settling: htmx:load handlers (local timestamps) run while settling and
  // can change the prepended messages' height.
  document.addEventListener("htmx:afterSwap", (event) => {
    if (olderPage(event) && fromBottom !== null) restore();
  });
  document.addEventListener("htmx:afterSettle", (event) => {
    if (!olderPage(event) || fromBottom === null) return;
    restore();
    fromBottom = null;
    // The activated control was replaced; give focus to the next one, or to
    // the pane once the oldest message is shown.
    const next = document.querySelector("#load-older a") || pane();
    next.focus({ preventScroll: true });
  });
})();
