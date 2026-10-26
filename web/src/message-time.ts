(() => {
  const formatter = new Intl.DateTimeFormat(document.documentElement.lang, {
    year: "numeric", month: "numeric", day: "numeric",
    hour: "numeric", minute: "numeric", second: "numeric",
  });
  const localized = new WeakSet<HTMLTimeElement>();
  const selector = "time[data-local-time]";
  const localize = (element: HTMLTimeElement) => {
    // htmx can report overlapping subtrees, including the initial body.
    if (localized.has(element)) return;
    const date = new Date(element.dateTime);
    if (Number.isNaN(date.getTime())) return;
    element.textContent = formatter.format(date);
    localized.add(element);
  };
  const localizeWithin = (root: Document | Element) => {
    if ((root as Element).matches?.(selector)) localize(root as HTMLTimeElement);
    root.querySelectorAll<HTMLTimeElement>(selector).forEach(localize);
  };
  localizeWithin(document);
  document.addEventListener("htmx:load", (event) => localizeWithin((event as CustomEvent<{ elt: Element }>).detail.elt));
})();
