// Marks a page reached by a push navigation from another page of the app
// (a link, or a form's redirect) before it is painted, so CSS can run the
// pill and panel arrival motion only then: never on a first load, a reload or
// Back/Forward. It reads the browser's navigation.activation and stores nothing.
(() => {
  const activation = window.navigation && navigation.activation;
  if (!activation || activation.navigationType !== "push" || !activation.from || !activation.from.url) return;
  const from = new URL(activation.from.url);
  if (from.origin !== location.origin) return;
  // The conversation's place is the path without a panel suffix.
  const panel = /\/(members|overview)$/;
  const place = (path) => path.replace(panel, "");
  const root = document.documentElement;
  const marks = { "rb-pill-in": "arrivePill", "rb-panel-in": "arrivePanel" };
  if (place(from.pathname) !== place(location.pathname)) root.dataset.arrivePill = "";
  if (panel.test(location.pathname) && !panel.test(from.pathname)) root.dataset.arrivePanel = "";
  // Each mark lasts one run of its animation: an element inserted later,
  // such as the pill of a live sidebar list, must not move.
  const done = (event) => {
    if (marks[event.animationName]) delete root.dataset[marks[event.animationName]];
  };
  document.addEventListener("animationend", done);
  document.addEventListener("animationcancel", done);
  // A live replacement can arrive while the pill still grows; the new list
  // shows its pill at rest.
  document.addEventListener("htmx:oobBeforeSwap", (event) => {
    if (event.target.id === "sidebar-channels") delete root.dataset.arrivePill;
  });
  // Nothing animates under reduced motion or in a hidden element, so no end
  // event would remove those marks: drop them once the page is parsed.
  document.addEventListener("DOMContentLoaded", () => {
    const running = new Set(document.getAnimations().map((animation) => animation.animationName));
    for (const [name, mark] of Object.entries(marks)) if (!running.has(name)) delete root.dataset[mark];
  });
})();
