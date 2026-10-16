(() => {
  // "Copy link" in a message's actions menu (#1159) copies the message's
  // absolute URL: the browser resolved it from the root-relative link, so
  // the markup, shared between members' streams, holds no origin. Without
  // the Clipboard API, with a modifier key, or when copying fails, the link
  // simply opens, as it does without JavaScript.
  document.addEventListener("click", (event) => {
    const link = event.target.closest?.("a[data-copy-link]");
    if (!link || !navigator.clipboard?.writeText || event.button !== 0) return;
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    navigator.clipboard.writeText(link.href).then(() => {
      // The menu shows the confirmation; the conversation's polite status
      // region announces it, as it announces live messages.
      link.dataset.copied = "";
      const label = link.querySelector("[data-copied-label]")?.textContent;
      document.getElementById("message-status")?.append(document.createTextNode(label + "\n"));
    }, () => location.assign(link.href));
  });
  // A closed menu forgets the confirmation, so it opens with "Copy link".
  document.addEventListener("toggle", (event) => {
    if (event.newState === "closed") event.target.querySelector?.("a[data-copied]")?.removeAttribute("data-copied");
  }, true);
})();
