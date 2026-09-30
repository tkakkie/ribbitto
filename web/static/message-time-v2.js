(() => {
  const formatter = new Intl.DateTimeFormat(document.documentElement.lang, {
    year: "numeric", month: "numeric", day: "numeric",
    hour: "numeric", minute: "numeric", second: "numeric",
  });
  const localized = new WeakSet();
  const selector = "time[data-local-time]";
  const localize = (element) => {
    // htmx can report overlapping subtrees, including the initial body.
    if (localized.has(element)) return;
    const date = new Date(element.dateTime);
    if (Number.isNaN(date.getTime())) return;
    element.textContent = formatter.format(date);
    localized.add(element);
  };
  const localizeWithin = (root) => {
    if (root.matches?.(selector)) localize(root);
    root.querySelectorAll(selector).forEach(localize);
  };
  localizeWithin(document);
  document.addEventListener("htmx:load", (event) => localizeWithin(event.detail.elt));
})();
