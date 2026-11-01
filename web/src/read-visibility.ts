// Visibility and DOM application are UX glue; htmx owns every request.
import {
  AppliedCursorAttribute, dispatchPanelCoverEvent, getOrganizationStream, getReadForm,
  getSidePanel, listenReadAppliedEvent, readAttribute, ReadFormID, ReadVisibleEvent,
} from "./contracts.gen.ts";
import { readCursor, readDue, retryDelay, settleRead } from "./read-cursor.ts";

type ReadForm = HTMLFormElement & { elements: HTMLFormControlsCollection & Record<"cursor", HTMLInputElement> };
type HtmxLoadEvent = CustomEvent<{ elt: Element }>;
type HtmxConfigRequestEvent = CustomEvent<{ triggeringEvent?: Event; parameters: Record<string, string> }>;
type HtmxBeforeRequestEvent = CustomEvent<{ requestConfig?: { triggeringEvent?: Event } }>;
type HtmxAfterRequestEvent = CustomEvent<{
  successful: boolean;
  xhr: { status: number; getResponseHeader(name: string): string | null };
}>;

(() => {
  const initialized = new WeakSet<HTMLFormElement>();
  let inFlight = false;
  let active: (() => void) | null = null;
  document.addEventListener("htmx:load", (native) => {
    const root = (native as HtmxLoadEvent).detail.elt;
    const form = (root.id === ReadFormID ? root : getReadForm(root)) as ReadForm | null;
    if (!form || initialized.has(form)) return;
    initialized.add(form);
    const stream = getOrganizationStream();
    const snapshot = form.elements.cursor.value;
    let attempted: bigint | null = null;
    let succeeded: bigint | null = null;
    let timer: ReturnType<typeof setTimeout> | null = null;
    let failures = 0;
    let navigating = false;
    // Mark as read's cursor until a request covering it succeeds: one action
    // reads everything shown, and its 429 retries pass a covering panel.
    let intent: string | null = null;
    const needed = () => !navigating && document.visibilityState === "visible" && form.isConnected;
    // A modal panel covers the conversation (an overlay, or a phone's sheet):
    // automatic reading waits, Mark as read never does. web/src/side-panel.ts
    // alone decides it; the synchronous panel-cover event asks it to publish
    // aria-modal for the current size first. Its listener is in place from
    // htmx:load, which follows every deferred script.
    const covered = () => {
      const panel = getSidePanel();
      if (panel) dispatchPanelCoverEvent(panel, {});
      return panel?.getAttribute("aria-modal") === "true";
    };
    const clearRetry = () => {
      if (timer !== null) clearTimeout(timer);
      timer = null;
    };
    const applied = () => (stream ? readAttribute(stream, AppliedCursorAttribute) : null);
    const latest = () => readCursor(snapshot, applied(), 0n, null);
    const cursor = () => readCursor(snapshot, applied(), attempted, intent);
    const visible = () => {
      if (!needed() || (intent === null && covered()) || inFlight || timer !== null) return;
      if (readDue(cursor(), succeeded)) htmx.trigger(form, ReadVisibleEvent);
      else intent = null;
    };
    const retry = Object.assign(() => {
      timer = null;
      visible();
    }, { readRetry: true as const });
    form.addEventListener("htmx:configRequest", (native) => {
      const event = native as HtmxConfigRequestEvent;
      if (event.detail.triggeringEvent?.type === "submit") intent = latest();
      const next = cursor();
      form.elements.cursor.value = next;
      event.detail.parameters.cursor = next;
    });
    form.addEventListener("htmx:beforeRequest", (native) => {
      const event = native as HtmxBeforeRequestEvent;
      // Mark as read is the newest intent: it replaces a pending retry rather
      // than waiting behind it, since leaving the page would drop that retry.
      if (event.detail.requestConfig?.triggeringEvent?.type === "submit" && needed() && !inFlight) {
        clearRetry();
      }
      if (!needed() || inFlight || timer !== null) {
        event.preventDefault();
        return;
      }
      inFlight = true;
      attempted = BigInt(cursor());
    });
    form.addEventListener("htmx:afterRequest", (native) => {
      const event = native as HtmxAfterRequestEvent;
      inFlight = false;
      // A replacement waits for the old form's request slot to be released.
      if (!form.isConnected) {
        queueMicrotask(() => active?.());
        return;
      }
      if (!event.detail.successful && event.detail.xhr.status === 429) {
        if (!needed()) return;
        const delay = retryDelay(failures++, event.detail.xhr.getResponseHeader("Retry-After"), Math.random(), Date.now());
        clearRetry();
        timer = setTimeout(retry, delay);
        return;
      }
      if (attempted === null) return;
      const outcome = settleRead({ succeeded, failures, intent }, attempted, event.detail.successful, latest());
      succeeded = outcome.succeeded;
      failures = outcome.failures;
      intent = outcome.intent;
      if (!outcome.flush) return;
      // htmx releases its request lock after afterRequest returns. Read the
      // newest cursor then, rather than queueing snapshots of older triggers.
      queueMicrotask(() => active?.());
    });
    const visibility = () => {
      if (!needed()) clearRetry();
      else visible();
    };
    const navigation = () => {
      navigating = true;
      clearRetry();
    };
    const restore = () => {
      navigating = false;
      visible();
    };
    const stopReadApplied = listenReadAppliedEvent(document, visible);
    const removed = new MutationObserver(() => {
      if (form.isConnected) return;
      clearRetry();
      removed.disconnect();
      document.removeEventListener("visibilitychange", visibility);
      stopReadApplied();
      window.removeEventListener("resize", visible);
      window.removeEventListener("pagehide", navigation);
      window.removeEventListener("pageshow", restore);
      if (active === visible) active = null;
    });
    removed.observe(document.body, { childList: true, subtree: true });
    document.addEventListener("visibilitychange", visibility);
    // Crossing a breakpoint can uncover the conversation with the panel open.
    window.addEventListener("resize", visible);
    window.addEventListener("pagehide", navigation);
    window.addEventListener("pageshow", restore);
    active = visible;
    visible();
  });
})();
