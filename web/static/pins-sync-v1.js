(() => {
  const focused = new WeakSet();
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
  document.addEventListener("htmx:sseBeforeMessage", (event) => {
    if (event.detail.type !== "pins") return;
    const incoming = new DOMParser().parseFromString(event.detail.data, "text/html").getElementById("pins-section");
    if (!incoming || stale(incoming)) event.preventDefault();
  });
  document.addEventListener("htmx:beforeSwap", (event) => {
    const control = event.detail.target;
    if (!control?.matches("[data-pin-control]")) return;
    // Compare the response's snapshot too: either transport can finish first.
    const incoming = new DOMParser().parseFromString(event.detail.serverResponse, "text/html").getElementById("pins-section");
    if (incoming && stale(incoming)) { event.detail.shouldSwap = false; return; }
    if (control.contains(document.activeElement)) focused.add(event.detail.xhr);
  });
  const swapped = (event) => {
    if (event.detail.target?.id === "pins-section") {
      document.querySelectorAll("[data-pin-control]").forEach(sync);
    }
    const control = document.getElementById(event.detail.target?.id);
    if (control?.matches("[data-pin-control]") && focused.has(event.detail.xhr)) {
      control.querySelector("button:not([hidden])").focus({ preventScroll: true });
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
