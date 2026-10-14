(() => {
  // Channels are ordered by name, so the current one and its nested topics
  // can sit far down a long sidebar (#922). This runs once, on load: live
  // list replacements happen inside the scrolling pane, which keeps its own
  // position, and moving it then would fight a member who has scrolled.
  const list = document.getElementById("sidebar-channels");
  // The pane is the sidebar's <aside>, or the list page's <main>.
  const pane = list?.closest("aside, main");
  if (!pane) return;
  // A listed selection is the row marked as the current page. An unlisted
  // topic falls back to its channel, the row that nests the topic list.
  const row = list.querySelector("[aria-current='page']") ??
    document.getElementById("sidebar-topics")?.closest("li")?.querySelector(":scope > a");
  if (!row) return;
  const port = pane.getBoundingClientRect();
  const box = row.getBoundingClientRect();
  if (box.top >= port.top && box.bottom <= port.bottom) return;
  // Centre the row, leaving its neighbours (a channel's topics) in view.
  // Setting scrollTop moves only this pane, instantly: never the window or
  // focus, and no motion that a reduced-motion preference would have to stop.
  pane.scrollTop += box.top - port.top - (pane.clientHeight - box.height) / 2;
})();
