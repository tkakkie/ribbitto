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
  // A post's success may arrive mid-composition, when rewriting the value
  // would break the composition: the sent text then waits for its end.
  const composing = new WeakSet();
  const pendingRemoval = new WeakMap();
  // Only the sent text leaves, and only if the textarea still starts with it.
  // setRangeText keeps the caret and selection on the remaining text and
  // fires no input event, so no typing signal follows.
  const removeSent = (body, sent) => {
    if (sent && body.value.startsWith(sent)) body.setRangeText("", 0, sent.length, "preserve");
  };
  document.addEventListener("compositionstart", (event) => {
    if (event.target.id === "message-body") composing.add(event.target);
  });
  document.addEventListener("compositionend", (event) => {
    const body = event.target;
    if (body.id !== "message-body") return;
    compositionEnded.set(body, event.timeStamp);
    composing.delete(body);
    if (pendingRemoval.has(body)) {
      removeSent(body, pendingRemoval.get(body));
      pendingRemoval.delete(body);
    }
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
  // A message's URL (decision 36) lands on the message its fragment names,
  // which the browser has brought into view; only other loads start at the
  // newest entry.
  const newest = () => {
    const list = document.getElementById("message-list");
    const target = document.getElementById(location.hash.slice(1));
    if (target && target.parentElement?.id === "message-items") return;
    if (list) list.scrollTop = list.scrollHeight;
  };
  newest();
  // Responses replace only #message-composer-state (#1173): the textarea
  // stays enabled and in place while a post is in flight.
  document.addEventListener("htmx:afterSwap", (event) => {
    const state = event.target;
    if (state.id !== "message-composer-state") return;
    const body = document.getElementById("message-body");
    body.setAttribute("aria-invalid", String(state.hasAttribute("data-invalid")));
    if (event.detail.xhr.status === 200) {
      const sent = event.detail.requestConfig.formData.get("body");
      if (composing.has(body)) pendingRemoval.set(body, sent);
      else removeSent(body, sent);
    }
    // Focus on the send button returns to the textarea. htmx disables the
    // button in flight, and Chrome then moves its focus to the body at the
    // next frame.
    const active = document.activeElement;
    if (active === body.form.querySelector("button") || active === document.body) body.focus({ preventScroll: true });
  });
})();
