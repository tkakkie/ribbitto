(() => {
  document.addEventListener("submit", (event) => {
    if (event.target.id !== "message-composer") return;
    const stream = document.getElementById("organization-stream");
    const cursor = event.target.elements.cursor;
    if (stream && cursor) cursor.value = stream.dataset.eventCursor;
  }, true);
  // Some phone IMEs end composition before dispatching the confirming Enter,
  // with neither isComposing nor 229. Keep that short event sequence local to
  // its textarea; a later deliberate Enter still sends normally.
  const compositionEnded = new WeakMap();
  document.addEventListener("compositionend", (event) => {
    if (event.target.id === "message-body") compositionEnded.set(event.target, event.timeStamp);
  });
  const command = (event) => {
    if (event.isComposing || event.keyCode === 229 || event.key !== "Enter") return;
    const ended = compositionEnded.get(event.target);
    if (ended !== undefined && event.timeStamp - ended < 100) return;
    return event.shiftKey ? "newline" : "send";
  };
  const submit = (form) => form.requestSubmit();
  document.addEventListener("keydown", (event) => {
    if (event.target.id !== "message-body" || command(event) !== "send") return;
    event.preventDefault();
    if (!event.repeat && !event.target.disabled) submit(event.target.form);
  });
  const signal = (event) => {
    if (event.target.id !== "message-body" || event.isComposing) return;
    const form = document.getElementById("typing-form");
    if (form && event.target.value.trim()) htmx.trigger(form, "typing-input");
  };
  document.addEventListener("input", signal);
  document.addEventListener("compositionend", signal);
  const newest = () => {
    const list = document.getElementById("message-list");
    if (list) list.scrollTop = list.scrollHeight;
  };
  newest();
  document.addEventListener("htmx:afterSettle", (event) => {
    if (event.detail.target?.id !== "message-composer") return;
    document.getElementById("message-body").focus({ preventScroll: true });
  });
})();
