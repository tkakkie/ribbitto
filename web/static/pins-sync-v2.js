(() => {
  const focused = new WeakSet();
  const inFlight = new WeakSet();
  document.addEventListener("submit", (event) => {
    const form = event.target;
    const button = event.submitter;
    if (!form.matches("[data-pin-control]") || !window.htmx || !button?.matches("[data-pin-action]")) return;
    event.preventDefault();
    if (inFlight.has(form)) return;
    inFlight.add(form);
    const clear = () => inFlight.delete(form);
    try {
      window.htmx.ajax("POST", button.getAttribute("formaction"), { source: button, target: form, swap: "outerHTML" }).then(clear, clear);
    } catch (error) {
      clear();
      throw error;
    }
  });
  // 2.0.7 sends afterRequest on the source (or its surviving ancestor after
  // a swap). Do not rely on its promise: an onload exception can leave it pending.
  const answered = (event) => inFlight.delete(event.detail.requestConfig?.elt.closest("[data-pin-control]"));
  document.addEventListener("htmx:afterRequest", answered);
  document.addEventListener("htmx:sendError", answered);
  const section = () => document.getElementById("pins-section");
  const stale = (incoming) => section() && BigInt(incoming.dataset.pinsSeq) < BigInt(section().dataset.pinsSeq);
  const sync = (control) => {
    const pins = section();
    if (!pins) return;
    control.dataset.pinned = String(pins.dataset.pinnedIds.split(" ").includes(control.dataset.pinControl));
    const active = control.contains(document.activeElement);
    for (const button of control.querySelectorAll("[data-pin-action]")) {
      button.hidden = button.dataset.pinAction !== control.dataset.pinned;
    }
    if (active) control.querySelector("button:not([hidden])").focus({ preventScroll: true });
  };
  document.addEventListener("htmx:configRequest", (event) => {
    if (event.detail.elt.matches("[data-pin-action]") && section()) event.detail.headers["X-Pins-Section"] = "true";
  });
  document.addEventListener("htmx:sseBeforeMessage", (event) => {
    if (event.detail.type !== "pins") return;
    const incoming = new DOMParser().parseFromString(event.detail.data, "text/html").getElementById("pins-section");
    if (!incoming || stale(incoming)) event.preventDefault();
  });
  document.addEventListener("htmx:oobBeforeSwap", (event) => {
    const incoming = event.detail.fragment.querySelector("#pins-section");
    // Keep the response's control and feedback even if a newer frame won.
    if (incoming && stale(incoming)) event.detail.shouldSwap = false;
  });
  document.addEventListener("htmx:beforeSwap", (event) => {
    const control = event.detail.target;
    if (!control?.matches("[data-pin-control]")) return;
    if (control.contains(document.activeElement)) focused.add(event.detail.xhr);
  });
  const swapped = (event) => {
    if ((event.detail.target ?? event.detail.elt)?.id === "pins-section") {
      document.querySelectorAll("[data-pin-control]").forEach(sync);
    }
    const control = document.getElementById(event.detail.target?.id);
    if (control?.matches("[data-pin-control]")) {
      sync(control);
      if (focused.has(event.detail.xhr)) control.querySelector("button:not([hidden])").focus({ preventScroll: true });
    }
  };
  document.addEventListener("htmx:afterSwap", swapped);
  document.addEventListener("htmx:oobAfterSwap", swapped);
  document.addEventListener("htmx:load", (event) => {
    const root = event.detail.elt;
    if (root.matches("[data-pin-control]")) sync(root);
    root.querySelectorAll("[data-pin-control]").forEach(sync);
  });
})();
