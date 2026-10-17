(() => {
  const pendingRequests = new Set();
  const pendingForms = () => [...document.querySelectorAll("#message-pending [data-pending-form]")];
  const dispatch = () => {
    if (pendingRequests.size || document.getElementById("message-composer")?.classList.contains("htmx-request")) return;
    const forms = pendingForms();
    if (forms.some(form => form.dataset.state === "failed" && form.dataset.unconfirmed)) return;
    const head = forms.find(form => form.dataset.state === "queued");
    if (head) htmx.trigger(head, "pending-send");
  };
  document.addEventListener("submit", (event) => {
    if (event.target.matches("[data-pending-form]")) {
      event.preventDefault();
      event.stopImmediatePropagation();
      const form = event.target;
      if (form.dataset.state !== "failed") return;
      // A refused send will now be stored after the later sends. An unknown
      // outcome keeps its place until its same-key retry resolves it.
      if (!form.dataset.unconfirmed) form.closest("li").parentElement.append(form.closest("li"));
      form.dataset.state = "queued";
      form.querySelector("[role=status]").textContent = "";
      form.querySelector("[data-pending-actions]").hidden = true;
      form.closest("li").querySelector("[data-pending-sending]").hidden = false;
      document.getElementById("message-body").focus({ preventScroll: true });
      dispatch();
      return;
    }
    if (event.target.id !== "message-composer") return;
    const stream = document.getElementById("organization-stream");
    const cursor = event.target.elements.cursor;
    if (stream && cursor) cursor.value = stream.dataset.eventCursor;
    const template = document.getElementById("message-pending-template");
    if (!template || !crypto.randomUUID || !event.target.elements.idempotency_key || !cursor) return;
    const form = event.target, body = form.elements.body;
    const list = document.getElementById("message-pending");
    // Both forms replace the same state; a stale ordinary answer must not
    // overwrite a pending send's fresh generation and key.
    if (form.classList.contains("htmx-request") || list.children.length >= 10) {
      event.preventDefault();
      event.stopImmediatePropagation();
      say(list.children.length >= 10 ? "capacity" : "busy");
      return;
    }
    const text = body.value.replace(/\r\n?/g, "\n").trim();
    if (!text || [...text].length > 4000) {
      if (list.firstElementChild || pendingRequests.size) {
        event.preventDefault();
        event.stopImmediatePropagation();
        say("busy");
      }
      return;
    }
    event.preventDefault();
    event.stopImmediatePropagation();
    const item = template.content.firstElementChild.cloneNode(true);
    const sending = item.querySelector("form");
    for (const name of ["idempotency_key", "composer", "cursor"]) sending.elements[name].value = form.elements[name]?.value || "";
    sending.elements.idempotency_key.value = crypto.randomUUID();
    sending.elements.body.value = body.value;
    const shownBody = item.querySelector("[data-pending-body]");
    shownBody.textContent = text;
    shownBody.id += "-" + sending.elements.idempotency_key.value;
    for (const button of sending.querySelectorAll("button")) button.setAttribute("aria-describedby", shownBody.id);
    sending.dataset.state = "queued";
    list.append(item);
    body.value = "";
    form.elements.idempotency_key.value = crypto.randomUUID();
    body.focus({ preventScroll: true });
    say("");
    reconcile();
    item.scrollIntoView({ block: "nearest" });
    htmx.process(sending);
    dispatch();
  }, true);
  const pendingForm = (event) => {
    const form = event.detail.requestConfig?.elt;
    return form?.matches("[data-pending-form]") ? form : null;
  };
  // Mutation observers run before paint: a response-first delivery never
  // paints both identities. Stream-first waits for the response's ID.
  const reconcile = () => {
    const list = document.getElementById("message-pending");
    for (const item of [...(list?.children || [])]) {
      if (item.dataset.postedMessage && document.getElementById(item.dataset.postedMessage)?.parentElement.id === "message-items") {
        item.remove();
        say("");
      }
    }
    const empty = document.getElementById("message-empty");
    if (empty) empty.style.display = list?.firstElementChild ? "none" : "";
  };
  const items = document.getElementById("message-items");
  if (items) new MutationObserver(reconcile).observe(items, { childList: true });
  document.addEventListener("htmx:sseBeforeMessage", (event) => {
    if (event.target.id !== "message-items" || event.detail.type !== "messages-moved") return;
    if (!items.dataset.topic) return;
    const payload = new DOMParser().parseFromString(event.detail.data, "text/html");
    if (payload.querySelector("ul")?.dataset.fromTopic !== items.dataset.topic) return;
    for (const form of pendingForms()) {
      const item = form.closest("li");
      if (item.dataset.postedMessage && payload.getElementById(item.dataset.postedMessage)) {
        item.remove();
        say("");
      }
    }
    reconcile();
  });
  document.addEventListener("click", (event) => {
    if (!event.target.closest("[data-pending-remove]")) return;
    event.target.closest("li").remove();
    say("");
    reconcile();
    dispatch();
    document.getElementById("message-body").focus({ preventScroll: true });
  });
  document.addEventListener("htmx:beforeRequest", (event) => {
    const form = pendingForm(event);
    if (!form) return;
    pendingRequests.add(form);
    form.dataset.state = "sending";
    // htmx dispatches completion on the form even after it is detached,
    // when the event can no longer bubble to document.
    form.addEventListener("htmx:afterRequest", (answer) => {
      completePending(answer, form);
      pendingRequests.delete(form);
      // Finish htmx's request cleanup before triggering the next form.
      queueMicrotask(dispatch);
    }, { once: true });
    document.getElementById("message-body").focus({ preventScroll: true });
    form.querySelector("[role=status]").textContent = "";
    form.querySelector("[data-pending-actions]").hidden = true;
    form.closest("li").querySelector("[data-pending-sending]").hidden = false;
  });
  document.addEventListener("htmx:beforeSwap", (event) => {
    if (pendingForm(event) && event.detail.xhr.status !== 200) event.detail.shouldSwap = false;
  });
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
    const pending = pendingForm(event);
    if (pending) {
      const item = pending.closest("li");
      item.dataset.postedMessage = state.dataset.postedMessage;
      // Read response routing once; retain only the confirmed ID on the item.
      // Ordered application proves an absent post has already moved away.
      const topic = items.dataset.topic;
      if (topic) {
        const applied = document.getElementById("organization-stream").dataset.appliedCursor;
        if (state.dataset.postedTopic !== topic ||
            (!items.querySelector("#" + state.dataset.postedMessage) && applied &&
             BigInt(applied) >= BigInt(state.dataset.postedSeq))) {
          item.remove();
          say("");
        }
      }
      reconcile();
      return;
    }
    const status = event.detail.xhr.status;
    if (status === 200) {
      const sent = event.detail.requestConfig.formData.get("body");
      say("");
      afterComposition(body, () => removeSent(body, sent));
    }
    // A 409 names the stored message for this key. The server sends its body
    // only when the retried text started with it: the earlier answer was
    // lost while the member typed on (#1179). If the start has been edited
    // since, the 409's error, which the help then omits, is said here.
    if (status === 409) {
      const sent = state.dataset.sentBody;
      say("");
      if (sent) afterComposition(body, () => say(removeSent(body, sent) ? "recovered" : "repeat"));
    }
    // Focus on the send button returns to the textarea. htmx disables the
    // button in flight, and Chrome then moves its focus to the body at the
    // next frame.
    const active = document.activeElement;
    if (active === body.form.querySelector("button") || active === document.body) body.focus({ preventScroll: true });
  });
  const completePending = (event, pending) => {
    const status = event.detail.xhr.status;
    if (status === 200) {
      pending.dataset.state = "confirmed";
      delete pending.dataset.unconfirmed;
      return;
    }
    pending.dataset.state = "failed";
    const alert = document.getElementById("message-send-alert");
    let name = "unconfirmed";
    if (status === 409) {
      name = "repeat";
      delete pending.dataset.unconfirmed;
      pending.elements.idempotency_key.value = crypto.randomUUID();
    } else if ([400, 404, 413, 422, 429].includes(status)) {
      name = pending.dataset.unconfirmed ? "stillUnconfirmed" : "notSent";
    } else pending.dataset.unconfirmed = "true";
    pending.querySelector("[role=status]").textContent = alert.dataset[name];
    pending.querySelector("[data-pending-actions]").hidden = false;
    pending.closest("li").querySelector("[data-pending-sending]").hidden = true;
  };
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
    // The post handler answers 400/404/413/422 before any write; 429 rejects
    // the request by spec. Other 4xx, 5xx, network errors or timeouts may
    // follow a commit.
    if (status === 400 || status === 404 || status === 413 || status === 422 || status === 429) {
      say(form.dataset.unconfirmedKey ? "stillUnconfirmed" : "notSent");
    } else {
      form.dataset.unconfirmedKey = key;
      say("unconfirmed");
    }
  });
})();
