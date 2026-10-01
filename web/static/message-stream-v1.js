(() => {
  document.addEventListener("htmx:sseBeforeMessage", (event) => {
    const items = event.target;
    if (items.id !== "message-items") return;
    const connection = items.closest("[sse-connect]");
    const incoming = new DOMParser().parseFromString(event.detail.data, "text/html").querySelector("li");
    if (!connection || !incoming) return;
    const existing = document.getElementById(incoming.id);
    const pane = document.getElementById("message-list");
    const atBottom = pane.scrollHeight - pane.scrollTop - pane.clientHeight <= 2;
    // Keep the extension's transport, but let htmx replace replay duplicates
    // instead of appending another item (or announcing it again).
    event.preventDefault();
    htmx.swap(existing || items, event.detail.data, {
      swapStyle: existing ? "outerHTML" : "beforeend", settleDelay: 0,
    }, {
      afterSettleCallback: () => {
        if (atBottom) pane.scrollTop = pane.scrollHeight;
        // Retain additions so a replay burst cannot overwrite an announcement
        // before assistive technology has observed it.
        if (!existing) {
          document.getElementById("message-status").append(
            document.createTextNode(incoming.dataset.announcement + "\n"),
          );
        }
      },
    });
    // A native reconnect sends Last-Event-ID. The extension recreates CLOSED
    // sources from this attribute, so that path also resumes after delivery.
    connection.dataset.eventCursor = event.detail.lastEventId;
    const url = new URL(connection.getAttribute("sse-connect"), location.href);
    url.searchParams.set("after", event.detail.lastEventId);
    connection.setAttribute("sse-connect", url.pathname + url.search);
  });
})();
