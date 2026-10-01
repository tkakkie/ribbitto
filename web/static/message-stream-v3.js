(() => {
  // Successful posts may arrive through the stream only after a reconnect.
  // Keep their scroll intent independently of later composer replacements.
  const pending = new Set();
  const scrollPosted = (id) => {
    if (!pending.has(id)) return false;
    const item = document.getElementById(id);
    if (!item) return false;
    pending.delete(id);
    item.scrollIntoView({ block: "nearest" });
    // Include the list's bottom padding so later live messages still follow.
    if (item === document.getElementById("message-items").lastElementChild) {
      const pane = document.getElementById("message-list");
      pane.scrollTop = pane.scrollHeight;
    }
    return true;
  };
  document.addEventListener("htmx:afterSettle", (event) => {
    const form = event.target;
    if (form.id !== "message-composer" || !form.dataset.postedMessage) return;
    const id = form.dataset.postedMessage;
    delete form.dataset.postedMessage;
    pending.add(id);
    // Delivery can precede the POST response; the item is then already here.
    scrollPosted(id);
  });
  const reset = (event) => {
    event.target.close();
    location.reload();
  };
  document.addEventListener("htmx:sseOpen", (event) => {
    event.detail.source.addEventListener("reset", reset, { once: true });
  });
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
        if (!scrollPosted(incoming.id) && atBottom) pane.scrollTop = pane.scrollHeight;
        // Retain recent additions for replay bursts without growing forever.
        // aria-relevant="additions" keeps removal of older entries silent.
        if (!existing) {
          const status = document.getElementById("message-status");
          status.append(
            document.createTextNode(incoming.dataset.announcement + "\n"),
          );
          while (status.childNodes.length > 10) status.firstChild.remove();
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
