(() => {
  "use strict";
  const buttons = "button[data-quote-selection]";
  let current = null;
  let text = "";

  function bodyOf(node) {
    return (node?.nodeType === Node.ELEMENT_NODE ? node : node?.parentElement)
      ?.closest('[id^="body-message-"]');
  }
  function bothEnds(body, selection) {
    if (!body) return false;
    const range = selection.getRangeAt(0);
    if (!body.contains(range.startContainer)) return false;
    if (body.contains(range.endContainer)) return true;
    // Triple-click may end at offset zero of the following element. Accept
    // only an empty tail, never text from another message or quote snapshot.
    const tail = range.cloneRange();
    tail.setStart(body, body.childNodes.length);
    return tail.cloneContents().textContent === "";
  }
  function nonempty(selection) {
    return !!selection && !selection.isCollapsed && selection.toString().trim() !== "";
  }
  function excludesQuotes(body, selection) {
    // Rich bodies may themselves contain blockquotes. A range crossing one
    // is excluded even when neither endpoint is inside the quote.
    return [...body.querySelectorAll("blockquote")].every(quote =>
      !selection.getRangeAt(0).intersectsNode(quote));
  }
  function selected(selection) {
    if (!nonempty(selection) || selection.rangeCount !== 1) return null;
    const body = bodyOf(selection.getRangeAt(0).startContainer);
    return bothEnds(body, selection) && excludesQuotes(body, selection) ? body : null;
  }
  // Mirror ExtractChatV1, independently of browser paragraph separators and
  // display names. Unknown markup or a cut/unresolved mention is not quotable.
  function serializeSelection(body, selectedRange) {
    const range = selectedRange.cloneRange();
    if (!body.contains(range.endContainer)) range.setEnd(body, body.childNodes.length);
    const mentionAt = node => (node.nodeType === Node.ELEMENT_NODE ? node : node.parentElement)
      ?.closest("[data-quote-handle]");
    if (mentionAt(range.startContainer) || mentionAt(range.endContainer)) return null;
    if (body.tagName === "P") return range.toString();
    function inline(node) {
      if (node.nodeType === Node.TEXT_NODE) return node.textContent;
      if (node.nodeType !== Node.ELEMENT_NODE) return null;
      if (node.hasAttribute("data-quote-handle")) {
        const handle = node.getAttribute("data-quote-handle");
        return handle ? "@" + handle : null;
      }
      if (node.tagName === "BR") return "\n";
      if (!["P", "PRE", "STRONG", "EM", "CODE", "A"].includes(node.tagName)) return null;
      return children(node, "");
    }
    function children(node, separator) {
      const parts = [...node.childNodes].map(inline);
      return parts.includes(null) ? null : parts.join(separator);
    }
    const fragment = range.cloneContents();
    const blocks = [...fragment.children].some(node => ["P", "PRE"].includes(node.tagName));
    return children(fragment, blocks ? "\n" : "");
  }
  function isSubmitter(button, body) {
    return !!body && button?.matches(buttons) && button.previousElementSibling === body;
  }
  function update() {
    const selection = document.getSelection();
    current = selected(selection);
    text = current ? selection.toString() : null;
    if (!text?.trim()) current = null;
    for (const button of document.querySelectorAll(buttons)) button.hidden = !isSubmitter(button, current);
  }
  function clear() {
    const input = document.querySelector('#quote-form input[name="quote_text"]');
    if (input) {
      input.disabled = true;
      input.value = "";
    }
  }
  document.addEventListener("selectionchange", update);
  document.addEventListener("htmx:afterSwap", update);
  function press(event) {
    if (!event.target.closest?.(buttons)) return;
    // Read before the default focus/selection action. Cancelling pointerdown
    // and mousedown retains the Range without replacing native click submit.
    update();
    event.preventDefault();
  }
  document.addEventListener("pointerdown", press);
  document.addEventListener("mousedown", press);
  document.addEventListener("submit", event => {
    clear();
    if (!event.submitter?.matches(buttons)) return;
    update();
    if (!isSubmitter(event.submitter, current)) {
      event.preventDefault();
      event.stopImmediatePropagation();
    }
  }, true);
  document.addEventListener("htmx:configRequest", event => {
    if (event.target.id !== "quote-form") return;
    clear();
    delete event.detail.parameters.quote_text;
    const button = event.detail.triggeringEvent?.submitter;
    if (!button?.matches(buttons)) return;
    // Always re-read: keyboard/programmatic submits need no pointer press,
    // and a changed or lost selection must never become a whole quote.
    update();
    if (!isSubmitter(button, current)) {
      event.preventDefault();
      return;
    }
    const input = event.target.querySelector('input[name="quote_text"]');
    input.value = text;
    input.disabled = false;
    // htmx has collected controls already; its parameter proxy replaces any
    // previous value, retaining exactly one field in this request body.
    event.detail.parameters.quote_text = input.value;
  });
  for (const name of ["htmx:beforeSend", "htmx:afterRequest", "htmx:afterSwap"]) {
    document.addEventListener(name, clear);
  }
})();
