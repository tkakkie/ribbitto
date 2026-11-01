import {
  getPinsSection,
  PinActionAttribute,
  PinControlAttribute,
  PinnedAttribute,
  PinnedIDsAttribute,
  PinsSectionID,
  PinsSeqAttribute,
  readAttribute,
  writeAttribute,
} from "./contracts.gen.ts";
import { isControlPinned, isStalePinsSeq } from "./pins-seq.ts";

(() => {
  const focused = new WeakSet<object>();
  const inFlight = new WeakSet<Element>();
  const pinControlSelector = `[${PinControlAttribute}]`;
  const pinActionSelector = `[${PinActionAttribute}]`;
  document.addEventListener("submit", (event: SubmitEvent) => {
    const form = event.target as Element | null;
    if (!form?.matches(pinControlSelector)) return;
    const button = event.submitter ?? form.querySelector<HTMLElement>(`${pinActionSelector}:not([hidden])`);
    if (!window.htmx || !button?.matches(pinActionSelector)) return;
    event.preventDefault();
    if (inFlight.has(form)) return;
    inFlight.add(form);
    const clear = () => {
      inFlight.delete(form);
    };
    try {
      window.htmx
        .ajax("POST", button.getAttribute("formaction"), { source: button, target: form, swap: "outerHTML", event })
        .then(clear, clear);
    } catch (error) {
      clear();
      throw error;
    }
  });
  // After a swap, detail.elt can be a surviving ancestor: requestConfig.elt
  // still finds the detached source's form. onLoadError can leave promises pending.
  const answered = (event: Event) => {
    const detail = (event as CustomEvent<{ elt?: Element; requestConfig?: { elt?: Element } }>).detail;
    const form = (detail.requestConfig?.elt ?? detail.elt)?.closest(pinControlSelector);
    if (form) inFlight.delete(form);
  };
  for (const name of ["afterRequest", "sendError", "timeout", "sendAbort", "responseError", "onLoadError"]) {
    document.addEventListener("htmx:" + name, answered);
  }
  const stale = (incoming: Element) => {
    const current = getPinsSection();
    return isStalePinsSeq(readAttribute(incoming, PinsSeqAttribute), current ? readAttribute(current, PinsSeqAttribute) : null);
  };
  const sync = (control: Element) => {
    const pins = getPinsSection();
    if (!pins) return;
    const pinned = String(isControlPinned(readAttribute(pins, PinnedIDsAttribute), readAttribute(control, PinControlAttribute)));
    writeAttribute(control, PinnedAttribute, pinned);
    const active = control.contains(document.activeElement);
    for (const button of control.querySelectorAll<HTMLElement>(pinActionSelector)) {
      button.hidden = readAttribute(button, PinActionAttribute) !== pinned;
    }
    if (active) control.querySelector<HTMLElement>("button:not([hidden])")?.focus({ preventScroll: true });
  };
  document.addEventListener("htmx:configRequest", (event) => {
    const detail = (event as CustomEvent<{ elt: Element; headers: Record<string, string> }>).detail;
    if (detail.elt.matches(pinActionSelector) && getPinsSection()) detail.headers["X-Pins-Section"] = "true";
  });
  document.addEventListener("htmx:sseBeforeMessage", (event) => {
    const detail = (event as CustomEvent<{ type: string; data: string }>).detail;
    if (detail.type !== "pins") return;
    const incoming = getPinsSection(new DOMParser().parseFromString(detail.data, "text/html"));
    if (!incoming || stale(incoming)) event.preventDefault();
  });
  document.addEventListener("htmx:oobBeforeSwap", (event) => {
    const detail = (event as CustomEvent<{ fragment: ParentNode; shouldSwap: boolean }>).detail;
    const incoming = getPinsSection(detail.fragment);
    // Keep the response's control and feedback even if a newer frame won.
    if (incoming && stale(incoming)) detail.shouldSwap = false;
  });
  document.addEventListener("htmx:beforeSwap", (event) => {
    const detail = (event as CustomEvent<{ target?: Element; xhr: object }>).detail;
    const control = detail.target;
    if (!control?.matches(pinControlSelector)) return;
    if (control.contains(document.activeElement)) focused.add(detail.xhr);
  });
  const swapped = (event: Event) => {
    const detail = (event as CustomEvent<{ target?: Element; elt?: Element; xhr?: object }>).detail;
    if ((detail.target ?? detail.elt)?.id === PinsSectionID) {
      document.querySelectorAll(pinControlSelector).forEach(sync);
    }
    const id = detail.target?.id;
    const control = id ? document.getElementById(id) : null;
    if (control?.matches(pinControlSelector)) {
      sync(control);
      if (detail.xhr && focused.has(detail.xhr)) {
        control.querySelector<HTMLElement>("button:not([hidden])")?.focus({ preventScroll: true });
      }
    }
  };
  document.addEventListener("htmx:afterSwap", swapped);
  document.addEventListener("htmx:oobAfterSwap", swapped);
  document.addEventListener("htmx:load", (event) => {
    const root = (event as CustomEvent<{ elt: Element }>).detail.elt;
    if (root.matches(pinControlSelector)) sync(root);
    root.querySelectorAll(pinControlSelector).forEach(sync);
  });
})();
