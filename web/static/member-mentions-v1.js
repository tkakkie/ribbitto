(() => {
  const selector = "bdi[data-member-id]";
  const highlight = (element) => {
    const member = element.closest("#organization-stream")?.dataset.member;
    element.toggleAttribute("data-own-mention", !!member && element.dataset.memberId === member);
  };
  const highlightWithin = (root) => {
    if (root.matches?.(selector)) highlight(root);
    root.querySelectorAll(selector).forEach(highlight);
  };
  // First render, live posts, Load older and moves all use htmx's load event.
  document.addEventListener("htmx:load", (event) => highlightWithin(event.detail.elt));
})();
