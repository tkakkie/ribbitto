(() => {
  // "Copy link" in a message's actions menu (#1159) copies the message's
  // absolute URL: the browser resolved it from the root-relative link, so
  // the markup, shared between members' streams, holds no origin. Without
  // the Clipboard API, with a modifier key, or when copying fails, the link
  // simply opens, as it does without JavaScript.
  // The copy a link's open menu is waiting for; closing the menu drops it,
  // so a copy that settles later shows nothing and the menu reopens with
  // "Copy link".
  const pending = new WeakMap();
  document.addEventListener("click", (event) => {
    const link = event.target.closest?.("a[data-copy-link]");
    if (!link || !navigator.clipboard?.writeText || event.button !== 0) return;
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    const copy = {};
    pending.set(link, copy);
    navigator.clipboard.writeText(link.href).then(() => {
      if (pending.get(link) !== copy) return;
      // The menu shows the confirmation; the conversation's polite status
      // region announces it, as it announces live messages, and keeps the
      // same ten entries, since an older page gets no live message to prune it.
      link.dataset.copied = "";
      const label = link.querySelector("[data-copied-label]")?.textContent;
      const status = document.getElementById("message-status");
      if (!status) return;
      status.append(document.createTextNode(label + "\n"));
      while (status.childNodes.length > 10) status.firstChild.remove();
    }, () => {
      // Only the copy the open menu still waits for falls back to opening
      // the link; a failure after the menu closed must not navigate.
      if (pending.get(link) === copy) location.assign(link.href);
    });
  });
  // A closing menu forgets the confirmation, so it reopens with "Copy link".
  // beforetoggle, unlike toggle, fires for each change: toggle events are
  // queued and a close followed by a reopen coalesces into one.
  document.addEventListener("beforetoggle", (event) => {
    if (event.newState !== "closed") return;
    for (const link of event.target.querySelectorAll?.("a[data-copy-link]") ?? []) {
      pending.delete(link);
      link.removeAttribute("data-copied");
    }
  }, true);
})();
