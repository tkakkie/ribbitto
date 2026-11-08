import * as dom from "./contracts.gen.ts";

type HistoryEvent = CustomEvent<{ target?: Element }>;

export function startMessageHistory() {
  // Older messages are prepended above the ones being read. Keep the
  // distance from the bottom of the pane so the reader stays in place.
  let fromBottom: number | null = null;
  const pane = () => document.getElementById(dom.MessageListID)!;
  const restore = () => {
    pane().scrollTop = pane().scrollHeight - fromBottom!;
  };
  const olderPage = (event: HistoryEvent) => event.detail.target?.id === dom.MessageItemsID;
  document.addEventListener("htmx:beforeSwap", (event) => {
    if (olderPage(event as HistoryEvent)) fromBottom = pane().scrollHeight - pane().scrollTop;
  });
  // Restore once on swap so the page does not jump, and again after
  // settling: htmx:load handlers (local timestamps) run while settling and
  // can change the prepended messages' height.
  document.addEventListener("htmx:afterSwap", (event) => {
    if (olderPage(event as HistoryEvent) && fromBottom !== null) restore();
  });
  document.addEventListener("htmx:afterSettle", (event) => {
    if (!olderPage(event as HistoryEvent) || fromBottom === null) return;
    restore();
    fromBottom = null;
    // The activated control was replaced; give focus to the next one, or to
    // the pane once the oldest message is shown.
    const next = document.querySelector<HTMLElement>(`${dom.loadOlderSelector()} a`) || pane();
    next.focus({ preventScroll: true });
  });
}
