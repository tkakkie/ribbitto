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
    if (!sent || !body.value.startsWith(sent)) return false;
    body.setRangeText("", 0, sent.length, "preserve");
    return true;
  };
  const afterComposition = (body, removal) => {
    if (composing.has(body)) pendingRemoval.set(body, removal);
    else removal();
  };
  // The alert's texts are server-rendered and localised; an empty name clears it.
  const say = (name) => {
    const alert = document.getElementById("message-send-alert");
    if (alert) alert.textContent = name ? alert.dataset[name] : "";
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
      pendingRemoval.get(body)();
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
    const status = event.detail.xhr.status;
    if (status === 200) {
      const sent = event.detail.requestConfig.formData.get("body");
      say("");
      afterComposition(body, () => removeSent(body, sent));
    }
    // A 409 names the stored message for this key: after a lost answer, the
    // textarea may hold that message followed by text typed since (#1179).
    if (status === 409) {
      const sent = state.dataset.sentBody;
      say("");
      afterComposition(body, () => removeSent(body, sent) && say("recovered"));
    }
    // Focus on the send button returns to the textarea. htmx disables the
    // button in flight, and Chrome then moves its focus to the body at the
    // next frame.
    const active = document.activeElement;
    if (active === body.form.querySelector("button") || active === document.body) body.focus({ preventScroll: true });
  });
  // Every failure keeps the text and the key, so sending again repeats this
  // send (decision 37 (b)). Only a 200 or a 409 comes after the server's key
  // lookup and brings a new key; until then an unconfirmed send stays marked
  // on its key, whatever a retry is answered (#1179).
  document.addEventListener("htmx:afterRequest", (event) => {
    const form = event.target;
    if (form.id !== "message-composer") return;
    const status = event.detail.xhr.status;
    const key = form.elements.idempotency_key?.value;
    if (form.dataset.unconfirmedKey !== key) delete form.dataset.unconfirmedKey;
    if (status === 200 || status === 409) return;
    // A 4xx is answered before any write (a proxy's 429 too); a 5xx may come
    // after the commit, and a network error or timeout may follow it.
    if (status >= 400 && status < 500) {
      say(form.dataset.unconfirmedKey ? "stillUnconfirmed" : "notSent");
    } else {
      form.dataset.unconfirmedKey = key;
      say("unconfirmed");
    }
  });
})();
