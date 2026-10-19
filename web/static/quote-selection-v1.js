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
    return !!body && body.contains(selection.anchorNode) && body.contains(selection.focusNode);
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
    const body = bodyOf(selection.anchorNode);
    return bothEnds(body, selection) && excludesQuotes(body, selection) ? body : null;
  }
  function isSubmitter(button, body) {
    return !!body && button?.matches(buttons) && button.previousElementSibling === body;
  }
  function update() {
    const selection = document.getSelection();
    current = selected(selection);
    text = current ? selection.toString() : "";
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
