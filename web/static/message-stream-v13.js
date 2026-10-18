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
    const state = event.target;
    if (state.id !== "message-composer-state" || !state.dataset.postedMessage) return;
    const id = state.dataset.postedMessage;
    delete state.dataset.postedMessage;
    pending.add(id);
    // Delivery can precede the POST response; the item is then already here.
    scrollPosted(id);
  });
  let stopped = false;
  const reset = (event) => {
    event.target.close();
    if (stopped) return;
    stopped = true;
    let timer = null;
    const notice = document.getElementById("stream-stopped");
    // Confirmed items await reload delivery; drafts do not hold reload.
    const atStake = () => {
      const alert = document.getElementById("message-send-alert");
      return document.getElementById("message-composer")?.classList.contains("htmx-request") ||
        (alert?.textContent && ["unconfirmed", "notSent", "stillUnconfirmed"].some(name => alert.textContent === alert.dataset[name])) ||
        document.querySelector('#message-pending [data-state="queued"], #message-pending [data-state="sending"], #message-pending [data-state="failed"]');
    };
    const decide = () => {
      const held = Boolean(atStake());
      if (notice) notice.hidden = !held;
      if (held) {
        clearTimeout(timer);
        timer = null;
      } else if (timer === null) {
        timer = setTimeout(() => {
          timer = null;
          // Recheck even if the mutation observer has not run yet.
          if (atStake()) { decide(); return; }
          observer.disconnect();
          location.reload();
        }, Math.random() * 5000);
      }
    };
    const observer = new MutationObserver(decide);
    const pending = document.getElementById("message-pending");
    if (pending) observer.observe(pending, { childList: true, subtree: true, attributes: true, attributeFilter: ["data-state"] });
    const composer = document.getElementById("message-composer");
    if (composer) observer.observe(composer, { childList: true, characterData: true, subtree: true, attributes: true, attributeFilter: ["class"] });
    decide();
  };
  const showApplied = () => {
    const connection = document.getElementById("organization-stream");
    if (document.visibilityState !== "visible" || !connection?.dataset.appliedCursor) return;
    connection.dataset.eventCursor = connection.dataset.appliedCursor;
    document.dispatchEvent(new Event("read-applied"));
  };
  document.addEventListener("visibilitychange", showApplied);
  const progress = (message) => {
    // CLOSED sources are recreated from this URL, without Last-Event-ID.
    const connection = document.getElementById("organization-stream");
    const url = new URL(connection.getAttribute("sse-connect"), location.href);
    if (BigInt(message.lastEventId) < BigInt(url.searchParams.get("after"))) return;
    url.searchParams.set("after", message.lastEventId);
    connection.setAttribute("sse-connect", url.pathname + url.search);
  };
  const resume = (message) => {
    const connection = document.getElementById("organization-stream");
    // Hidden applications may resume replay, but reading/composing must wait
    // until this DOM is shown. Duplicate deliveries cannot lower either cursor.
    const applied = connection.dataset.appliedCursor || connection.dataset.eventCursor;
    if (BigInt(message.lastEventId) < BigInt(applied)) return;
    connection.dataset.appliedCursor = message.lastEventId;
    progress(message);
    showApplied();
  };
  document.addEventListener("htmx:sseOpen", (event) => {
    const connection = event.target;
    if (connection.id !== "organization-stream") return;
    const source = event.detail.source;
    source.addEventListener("reset", reset, { once: true });
    source.addEventListener("checkpoint", progress);
  });
  const applyMove = (items, data, payload) => {
    const moved = payload.querySelectorAll("li");
    const routing = payload.querySelector("ul").dataset;
    const topic = items.dataset.topic;
    // This boundary changes only with Load older's server-rendered control.
    // Zero means everything older is loaded, so every moved item is admitted.
    const oldest = BigInt(document.getElementById("load-older").dataset.oldestSeq);
    for (const item of moved) {
      const target = document.getElementById(item.id);
      if (topic === routing.fromTopic) {
        const divider = document.getElementById("unread-divider");
        if (target && divider?.dataset.eventSeq === target.dataset.eventSeq) divider.remove();
        target?.remove();
        continue;
      }
      if (target) {
        htmx.swap(target, data, { swapStyle: "outerHTML", settleDelay: 0 }, { select: "#" + item.id });
      } else if (topic === routing.toTopic && BigInt(item.dataset.eventSeq) >= oldest) {
        const next = Array.from(items.children).find((child) => BigInt(child.dataset.eventSeq) > BigInt(item.dataset.eventSeq));
        htmx.swap(next || items, data, { swapStyle: next ? "beforebegin" : "beforeend", settleDelay: 0 }, { select: "#" + item.id });
      }
    }
    // Replacement clears selection; refresh single-source constraints too.
    document.getElementById("branch-to")?.dispatchEvent(new Event("change", { bubbles: true }));
  };
  // History may have taken its snapshot before a move. Retain only deliveries
  // that cross an in-flight request, including moves applied to loaded items.
  const historyMoves = new Map();
  document.addEventListener("htmx:beforeSend", (event) => {
    if (event.detail.target?.id === "message-items" && event.detail.elt.closest("#load-older")) {
      historyMoves.set(event.detail.xhr, []);
    }
  });
  document.addEventListener("htmx:afterSwap", (event) => {
    const moves = historyMoves.get(event.detail.xhr);
    if (!moves || event.detail.target?.id !== "message-items") return;
    // Both the out-of-band control and the prepended items are now swapped.
    // Delete first: the htmx swaps below emit their own afterSwap events.
    historyMoves.delete(event.detail.xhr);
    for (const data of moves) {
      applyMove(event.detail.target, data, new DOMParser().parseFromString(data, "text/html"));
    }
  });
  document.addEventListener("htmx:afterRequest", (event) => {
    // Load older swaps synchronously; also release failed/aborted/no-swap reads.
    historyMoves.delete(event.detail.xhr);
  });
  document.addEventListener("htmx:sseBeforeMessage", (event) => {
    const items = event.target;
    if (items.id !== "message-items") return;
    const payload = new DOMParser().parseFromString(event.detail.data, "text/html");
    const incoming = payload.querySelector("li");
    if (!incoming) return;
    if (event.detail.type === "messages-moved") {
      event.preventDefault();
      for (const moves of historyMoves.values()) moves.push(event.detail.data);
      applyMove(items, event.detail.data, payload);
      resume(event.detail);
      return;
    }
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
        resume(event.detail);
        if (!scrollPosted(incoming.id) && atBottom) pane.scrollTop = pane.scrollHeight;
        if (existing) document.getElementById("branch-to")?.dispatchEvent(new Event("change", { bubbles: true }));
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
  });
})();
