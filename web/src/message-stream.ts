import * as dom from "./contracts.gen.ts";
import { admitsMove, higherSequence, announcementOverflow,
  resetHeld, resetDelay, pageHide, finishPageHide, parkSource, restoreSource, forgetSource, type SourceState } from "./stream-cursor.ts";

import { streamProgressAdapter } from "./stream-progress-adapter.ts";

// These structural read contracts describe htmx/extension inputs, not new transports.
type StreamSource = EventTarget & { readyState: number; close(): void };
type HtmxEvent = CustomEvent<{ source: StreamSource; target?: HTMLElement; elt: HTMLElement;
  xhr: object; data: string; type: string; lastEventId: string }> & { target: HTMLElement };
// Dataset absence is undefined (unlike getAttribute's null), including during BigInt parsing.
const data = (element: Element, name: dom.ContractAttribute): string | undefined => dom.readAttribute(element, name) ?? undefined;

(() => {
  const progress = streamProgressAdapter();
  const sources = new WeakSet<StreamSource>();
  let parked: StreamSource | null = null;
  let sourceState: SourceState = { hiding: false, parked: false };
  document.addEventListener("htmx:sseClose", (native) => {
    const event = native as HtmxEvent;
    // Keep the source/detail read short-circuited outside persisted cleanup.
    if (!sourceState.hiding) return;
    const transition = parkSource(sourceState, event.detail.source.readyState);
    if (transition.action === "park") parked = event.detail.source;
    // An accessor-backed event can return a falsy source on the second read.
    sourceState = { ...transition.state, parked: Boolean(parked) };
  });
  window.addEventListener("pagehide", (event) => {
    const transition = pageHide(sourceState, event.persisted);
    sourceState = transition.state;
    if (transition.action !== "cleanup") return;
    try {
      htmx.trigger(dom.getOrganizationStream()!, "htmx:beforeCleanupElement");
    } finally {
      sourceState = finishPageHide(sourceState);
    }
  });
  window.addEventListener("pageshow", (event) => {
    const transition = restoreSource(sourceState, event.persisted);
    sourceState = transition.state;
    if (transition.action !== "error") return;
    // The extension retries CLOSED sources and rebinds their children.
    parked!.dispatchEvent(new Event("error"));
    sourceState = forgetSource(sourceState);
    parked = null;
  });
  // Successful posts may arrive through the stream only after a reconnect.
  // Keep their scroll intent independently of later composer replacements.
  const pending = new Set<string>();
  const scrollPosted = (id: string) => {
    if (!pending.has(id)) return false;
    const item = document.getElementById(id);
    if (!item) return false;
    pending.delete(id);
    item.scrollIntoView({ block: "nearest" });
    // Include the list's bottom padding so later live messages still follow.
    if (item === dom.getMessageItems()!.lastElementChild) {
      const pane = dom.getMessageList()!;
      pane.scrollTop = pane.scrollHeight;
    }
    return true;
  };
  document.addEventListener("htmx:afterSettle", (native) => {
    const event = native as HtmxEvent;
    const state = event.target;
    if (state.id !== dom.MessageComposerStateID || !data(state, dom.PostedMessageAttribute)) return;
    const id = data(state, dom.PostedMessageAttribute)!;
    state.removeAttribute(dom.PostedMessageAttribute);
    pending.add(id);
    // Delivery can precede the POST response; the item is then already here.
    scrollPosted(id);
  });
  let pendingHold = false;
  let pendingChanged = () => {};
  document.addEventListener(dom.MessagePendingChangedEvent, (native) => {
    const event = native as CustomEvent<dom.MessagePendingChangedDetail>;
    // Retain both detail reads, including an accessor-backed external event.
    if (typeof event.detail?.holdsReload !== "boolean") return;
    pendingHold = event.detail.holdsReload;
    pendingChanged();
  });
  document.dispatchEvent(new CustomEvent<dom.MessagePendingQueryDetail>(dom.MessagePendingQueryEvent, { detail: {} }));
  let stopped = false;
  const reset = (event: Event) => {
    progress.report({ type: "terminal" });
    (event.target as StreamSource).close();
    if (stopped) return;
    stopped = true;
    let timer: ReturnType<typeof setTimeout> | null = null;
    let reloading = false;
    const notice = dom.getStreamStopped();
    // Confirmed items await reload delivery; drafts do not hold reload.
    const atStake = () => {
      const alert = dom.getMessageSendAlert();
      const request = dom.getMessageComposer()?.classList.contains("htmx-request");
      // Do not read alert attributes when a request already holds the guard.
      const failed = !request && Boolean(alert?.textContent &&
        ([dom.UnconfirmedAttribute, dom.NotSentAttribute, dom.StillUnconfirmedAttribute] as const)
          .some(name => alert.textContent === data(alert, name)));
      return resetHeld(request, failed, pendingHold);
    };
    const decide = () => {
      if (reloading) return;
      const held = Boolean(atStake());
      if (notice) notice.hidden = !held;
      if (held) {
        clearTimeout(timer as number);
        timer = null;
      } else if (timer === null) {
        timer = setTimeout(() => {
          timer = null;
          // Recheck even if the mutation observer has not run yet.
          if (atStake()) { decide(); return; }
          reloading = true;
          pendingChanged = () => {};
          observer.disconnect();
          location.reload();
        }, resetDelay(Math.random()));
      }
    };
    const observer = new MutationObserver(decide);
    pendingChanged = () => queueMicrotask(decide);
    const composer = dom.getMessageComposer();
    if (composer) observer.observe(composer, { childList: true, characterData: true, subtree: true, attributes: true, attributeFilter: ["class"] });
    decide();
  };
  document.addEventListener("visibilitychange", progress.show);
  // Native reconnects reopen the same source, so keep one listener identity.
  const onCheckpoint = (event: Event) => progress.checkpoint((event as MessageEvent).lastEventId);
  document.addEventListener("htmx:sseOpen", (native) => {
    const event = native as HtmxEvent;
    const connection = event.target;
    if (connection.id !== dom.OrganizationStreamID) return;
    const source = event.detail.source;
    if (!sources.has(source)) {
      sources.add(source);
      progress.connectionCreated(connection.getAttribute("sse-connect")!);
      for (const kind of ["message", "messages-moved", "checkpoint", "reset", "unread", "sidebar", "presence", "typing", "pins"]) {
        source.addEventListener(kind, event => progress.report({ type: "frame-dispatched", kind,
          lastEventId: (event as MessageEvent).lastEventId }), { capture: true });
      }
    }
    source.addEventListener("reset", reset, { once: true });
    source.addEventListener("checkpoint", onCheckpoint);
  });
  const applyMove = (items: HTMLElement, markup: string, payload: Document) => {
    const moved = payload.querySelectorAll("li");
    const routing = payload.querySelector("ul")!;
    // Retain the routing-container fault before parsing a malformed history boundary.
    void routing.dataset;
    const topic = data(items, dom.TopicAttribute);
    // This boundary changes only with Load older's server-rendered control.
    // Zero means everything older is loaded, so every moved item is admitted.
    const oldest = BigInt(data(dom.getLoadOlder()!, dom.OldestSeqAttribute)!);
    for (const item of moved) {
      const target = document.getElementById(item.id);
      if (topic === data(routing, dom.FromTopicAttribute)) {
        const divider = dom.getUnreadDivider();
        if (target && (divider ? data(divider, dom.EventSeqAttribute) : undefined) === data(target, dom.EventSeqAttribute)) divider!.remove();
        target?.remove();
        continue;
      }
      if (target) {
        htmx.swap(target, markup, { swapStyle: "outerHTML", settleDelay: 0 }, { select: "#" + item.id });
      } else if (topic === data(routing, dom.ToTopicAttribute) && admitsMove(data(item, dom.EventSeqAttribute), oldest)) {
        const next = Array.from(items.children).find((child) => higherSequence(data(child, dom.EventSeqAttribute), data(item, dom.EventSeqAttribute)));
        htmx.swap(next || items, markup, { swapStyle: next ? "beforebegin" : "beforeend", settleDelay: 0 }, { select: "#" + item.id });
      }
    }
    // Replacement clears selection; refresh single-source constraints too.
    dom.getBranchTo()?.dispatchEvent(new Event("change", { bubbles: true }));
  };
  const observeApplication = (id: string, reason: "apply-exception" | "history-reapply", apply: () => void) => {
    const failed = () => progress.report({ type: "failed", reason, id });
    document.addEventListener("htmx:swapError", failed);
    try { apply(); } catch (error) { failed(); throw error; }
    finally { document.removeEventListener("htmx:swapError", failed); }
  };
  // History may have taken its snapshot before a move. Retain only deliveries
  // that cross an in-flight request, including moves applied to loaded items.
  const historyMoves = new Map<object, { markup: string; id: string }[]>();
  document.addEventListener("htmx:beforeSend", (native) => {
    const event = native as HtmxEvent;
    if (event.detail.target?.id === dom.MessageItemsID && event.detail.elt.closest(dom.loadOlderSelector())) {
      historyMoves.set(event.detail.xhr, []);
    }
  });
  document.addEventListener("htmx:afterSwap", (native) => {
    const event = native as HtmxEvent;
    const moves = historyMoves.get(event.detail.xhr);
    if (!moves || event.detail.target?.id !== dom.MessageItemsID) return;
    // Both the out-of-band control and the prepended items are now swapped.
    // Delete first: the htmx swaps below emit their own afterSwap events.
    historyMoves.delete(event.detail.xhr);
    for (const move of moves) {
      observeApplication(move.id, "history-reapply", () =>
        applyMove(event.detail.target!, move.markup, new DOMParser().parseFromString(move.markup, "text/html")));
    }
  });
  document.addEventListener("htmx:afterRequest", (native) => {
    const event = native as HtmxEvent;
    // Load older swaps synchronously; also release failed/aborted/no-swap reads.
    historyMoves.delete(event.detail.xhr);
  });
  document.addEventListener("htmx:sseBeforeMessage", (native) => {
    const event = native as HtmxEvent;
    const items = event.target;
    if (items.id !== dom.MessageItemsID) return;
    const payload = new DOMParser().parseFromString(event.detail.data, "text/html");
    const incoming = payload.querySelector("li");
    if (!incoming) {
      progress.report({ type: "failed", reason: "decode-missing-item", id: event.detail.lastEventId });
      return;
    }
    if (event.detail.type === "messages-moved") {
      event.preventDefault();
      for (const moves of historyMoves.values()) moves.push({ markup: event.detail.data, id: event.detail.lastEventId });
      observeApplication(event.detail.lastEventId, "apply-exception", () => applyMove(items, event.detail.data, payload));
      progress.applied(event.detail.lastEventId);
      return;
    }
    const existing = document.getElementById(incoming.id);
    const pane = dom.getMessageList()!;
    const atBottom = pane.scrollHeight - pane.scrollTop - pane.clientHeight <= 2;
    // Keep the extension's transport, but let htmx replace replay duplicates
    // instead of appending another item (or announcing it again).
    event.preventDefault();
    observeApplication(event.detail.lastEventId, "apply-exception", () => htmx.swap(existing || items, event.detail.data, {
      swapStyle: existing ? "outerHTML" : "beforeend", settleDelay: 0,
    }, {
      afterSettleCallback: () => {
        progress.applied(event.detail.lastEventId);
        if (!scrollPosted(incoming.id) && atBottom) pane.scrollTop = pane.scrollHeight;
        if (existing) dom.getBranchTo()?.dispatchEvent(new Event("change", { bubbles: true }));
        // Retain recent additions for replay bursts without growing forever.
        // aria-relevant="additions" keeps removal of older entries silent.
        if (!existing) {
          const status = dom.getMessageStatus()!;
          status.append(
            document.createTextNode(data(incoming, dom.AnnouncementAttribute) + "\n"),
          );
          while (announcementOverflow(status.childNodes.length)) status.firstChild!.remove();
        }
      },
    }));
  });
})();
