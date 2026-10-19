(() => {
  // Safari's keyboard shrinks/pans only the visual viewport, not dvh.
  const viewport = window.visualViewport;
  const updateViewport = () => {
    const style = document.documentElement.style;
    for (const [name, value] of [["--composer-viewport-h", viewport?.height], ["--composer-viewport-top", viewport?.offsetTop]]) {
      const next = value === undefined ? "" : value + "px";
      if (style.getPropertyValue(name) === next) continue;
      if (next) style.setProperty(name, next);
      else style.removeProperty(name);
    }
  };
  updateViewport();
  viewport?.addEventListener("resize", updateViewport);
  viewport?.addEventListener("scroll", updateViewport);
  window.addEventListener("resize", updateViewport);
  window.addEventListener("orientationchange", updateViewport);
  const sendEvent = (name, detail) => document.getElementById("message-body").dispatchEvent(new CustomEvent(name, { bubbles: true, detail }));
  const pendingRequests = new Set();
  let rememberPost = () => {};
  // An edited send keeps its identity even when another queue response swaps
  // the hidden controls. Only sending or emptying this text releases it.
  let restoredKey = null;
  const updateEdits = () => {
    const body = document.getElementById("message-body");
    const occupied = body.value !== "" || composing.has(body);
    for (const form of pendingForms()) {
      const edit = form.querySelector("[data-pending-edit]");
      const help = form.querySelector("[data-pending-edit-help]");
      edit.disabled = occupied || form.dataset.state !== "failed";
      help.hidden = !occupied;
      edit.setAttribute("aria-describedby", form.closest("li").querySelector("[data-pending-body]").id + (occupied ? " " + help.id : ""));
    }
  };
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
    // Quote shares the composer's htmx lock; guard the pending/plain paths too,
    // before either can submit stale quote state or clear the draft.
    if (document.getElementById("quote-form")?.classList.contains("htmx-request")) {
      event.preventDefault();
      event.stopImmediatePropagation();
      return;
    }
    const stream = document.getElementById("organization-stream");
    const cursor = event.target.elements.cursor;
    if (stream && cursor) cursor.value = stream.dataset.eventCursor;
    const template = document.getElementById("message-pending-template");
    const inPlace = () => sendEvent("message-sending", { key: event.target.elements.idempotency_key?.value, body: event.target.elements.body.value, inPlace: true });
    if (!template || !crypto.randomUUID || !event.target.elements.idempotency_key || !cursor) {
      if (!event.target.classList.contains("htmx-request")) inPlace();
      return;
    }
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
    if (form.elements.quote || !text || [...text].length > 4000) {
      if (list.firstElementChild || pendingRequests.size) {
        event.preventDefault();
        event.stopImmediatePropagation();
        say("busy");
      } else inPlace();
      return;
    }
    event.preventDefault();
    event.stopImmediatePropagation();
    const item = template.content.firstElementChild.cloneNode(true);
    const sending = item.querySelector("form");
    for (const name of ["idempotency_key", "composer", "cursor"]) sending.elements[name].value = form.elements[name]?.value || "";
    sending.elements.idempotency_key.value = restoredKey || crypto.randomUUID();
    if (restoredKey && form.dataset.unconfirmedKey === restoredKey) sending.dataset.unconfirmed = "true";
    delete form.dataset.unconfirmedKey;
    restoredKey = null;
    sending.elements.body.value = body.value;
    const shownBody = item.querySelector("[data-pending-body]");
    shownBody.textContent = text;
    shownBody.id += "-" + sending.elements.idempotency_key.value;
    sending.querySelector("[data-pending-edit-help]").id += "-" + sending.elements.idempotency_key.value;
    for (const button of sending.querySelectorAll("button")) button.setAttribute("aria-describedby", shownBody.id);
    sending.dataset.state = "queued";
    list.append(item);
    sendEvent("message-sending", { key: sending.elements.idempotency_key.value, body: body.value, inPlace: false });
    body.value = "";
    form.elements.idempotency_key.value = crypto.randomUUID();
    body.focus({ preventScroll: true });
    say("");
    reconcile();
    updateEdits();
    item.scrollIntoView({ block: "nearest" });
    rememberPost();
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
  document.addEventListener("htmx:configRequest", (event) => {
    if (event.target.id !== "quote-form") return;
    // Closing the popover restores focus, clearing htmx's remembered button.
    // The native submit event retains it, including for live-delivered menus.
    const button = event.detail.triggeringEvent?.submitter;
    if (button?.name) event.detail.parameters[button.name] = button.value;
  });
  document.addEventListener("click", (event) => {
    event.target.closest('[form="quote-form"]')?.closest("[popover]")?.hidePopover();
    const edit = event.target.closest("[data-pending-edit]");
    if (!edit && !event.target.closest("[data-pending-remove]")) return;
    const body = document.getElementById("message-body");
    const form = event.target.closest("form");
    if (edit) {
      if (body.value || composing.has(body) || form.dataset.state !== "failed") return;
      body.value = form.elements.body.value;
      restoredKey = form.elements.idempotency_key.value;
      body.form.elements.idempotency_key.value = restoredKey;
      if (form.dataset.unconfirmed) body.form.dataset.unconfirmedKey = restoredKey;
      else delete body.form.dataset.unconfirmedKey;
      body.setSelectionRange(body.value.length, body.value.length);
    }
    sendEvent("message-pending-action", { key: form.elements.idempotency_key.value, kind: edit ? "edit" : "remove" });
    event.target.closest("li").remove();
    updateEdits();
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
    if (event.target.id !== "message-body") return;
    composing.add(event.target);
    updateEdits();
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
    updateEdits();
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
  // Native caret scrolling can precede field-sizing layout, including a
  // select-all replacement at the cap. Reveal the end using the final size.
  const revealEnd = (body) => {
    if (document.activeElement === body && body.selectionStart === body.value.length && body.selectionEnd === body.value.length) {
      body.scrollTop = body.scrollHeight;
    }
  };
  const body = document.getElementById("message-body");
  let quoteResized = false;
  const updateQuoteHeight = () => {
    if (!body) return;
    const quote = document.getElementById("composer-quote");
    const height = quote?.getBoundingClientRect().height || 0;
    // A visible chip adds one flex gap as well as its own height.
    const next = (height ? height + parseFloat(getComputedStyle(body.form).rowGap) : 0) + "px";
    if (body.form.style.getPropertyValue("--composer-quote-h") === next) return;
    body.form.style.setProperty("--composer-quote-h", next);
    // Measuring a removed chip can briefly grow the list and clamp its scroll,
    // even when the updated cap brings the list back to the same height.
    quoteResized = true;
  };
  updateQuoteHeight();
  // The first measurement is the initial layout, not a change to restore from.
  quoteResized = false;
  document.addEventListener("input", (event) => {
    if (event.target.id !== "message-body") return;
    requestAnimationFrame(() => revealEnd(event.target));
    if (restoredKey && event.target.value === "") {
      restoredKey = null;
      delete event.target.form.dataset.unconfirmedKey;
      event.target.form.elements.idempotency_key.value = crypto.randomUUID();
    }
    updateEdits();
  });
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
  const pane = document.getElementById("message-list");
  if (pane) {
    let height = pane.clientHeight, position, settling = false;
    const remember = () => {
      const top = pane.getBoundingClientRect().top;
      let item;
      for (const candidate of pane.querySelectorAll("#message-items > li, #message-pending > li")) {
        if (candidate.getBoundingClientRect().bottom > top) { item = candidate; break; }
      }
      position = { newest: pane.scrollHeight - pane.scrollTop - pane.clientHeight <= 2,
        item, offset: item ? item.getBoundingClientRect().top - top : 0 };
    };
    remember();
    rememberPost = () => { height = pane.clientHeight; remember(); };
    // A resize can clamp scrollTop before its scroll event. Keep the state
    // from the old height until the observer has restored the reader.
    pane.addEventListener("scroll", () => { if (!quoteResized && pane.clientHeight === height) remember(); });
    const resize = new ResizeObserver(() => {
      updateQuoteHeight();
      if (pane.clientHeight !== height || quoteResized) {
        quoteResized = false;
        height = pane.clientHeight;
        // Native resize scrolling need not equal a clamp of the old top.
        // Restore the saved intent instead of treating that motion as a reader scroll.
        if (!settling) {
          if (position.newest) pane.scrollTop = pane.scrollHeight;
          else if (position.item?.isConnected) pane.scrollTop +=
            position.item.getBoundingClientRect().top - pane.getBoundingClientRect().top - position.offset;
        }
        remember();
      }
      if (body) revealEnd(body);
    });
    resize.observe(pane);
    if (body) { resize.observe(body); resize.observe(body.form); }
    document.addEventListener("htmx:afterSwap", (event) => {
      if (event.target.id === "message-composer-state" && event.target.dataset.postedMessage) settling = true;
    });
    document.addEventListener("htmx:afterSettle", (event) => {
      // History restores and the stream's afterSettleCallback scroll run
      // synchronously after this listener. Save their result before resizing.
      if (pane.contains(event.target)) queueMicrotask(rememberPost);
      if (event.target.id !== "message-composer-state" || !settling) return;
      // The stream's scrollPosted runs in this same settle event. Its scroll
      // wins even when clearing the sent text also resizes the pane.
      queueMicrotask(() => { rememberPost(); settling = false; });
    });
  }
  // Responses replace only #message-composer-state (#1173): the textarea
  // stays enabled and in place while a post is in flight.
  document.addEventListener("htmx:afterSwap", (event) => {
    const state = event.target;
    if (state.id === "composer-quote") {
      updateQuoteHeight();
      document.getElementById("message-body").focus({ preventScroll: true });
      return;
    }
    if (state.id !== "message-composer-state") return;
    const body = document.getElementById("message-body");
    body.setAttribute("aria-invalid", String(state.hasAttribute("data-invalid")));
    const pending = pendingForm(event);
    // Guard against a future ordinary-send path retaining an edited key.
    if (!pending && [200, 409].includes(event.detail.xhr.status)) restoredKey = null;
    if (restoredKey) body.form.elements.idempotency_key.value = restoredKey;
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
      if (state.dataset.postedMessage) sendEvent("message-sent", { key: pending.elements.idempotency_key.value, message: state.dataset.postedMessage });
      return;
    }
    // The stream consumes this attribute at settle, possibly before IME ends.
    const message = state.dataset.postedMessage;
    const sentEvent = () => { if (message) sendEvent("message-sent", { key: event.detail.requestConfig.formData.get("idempotency_key"), message }); };
    const status = event.detail.xhr.status;
    if (status === 200) {
      const sent = event.detail.requestConfig.formData.get("body");
      say("");
      afterComposition(body, () => { removeSent(body, sent); updateEdits(); sentEvent(); });
    }
    // A 409 names the stored message for this key. The server sends its body
    // only when the retried text started with it: the earlier answer was
    // lost while the member typed on (#1179). If the start has been edited
    // since, the 409's error, which the help then omits, is said here.
    if (status === 409) {
      const sent = state.dataset.sentBody;
      say("");
      afterComposition(body, () => { if (sent) say(removeSent(body, sent) ? "recovered" : "repeat"); sentEvent(); });
    }
    // Focus on the send button returns to the textarea. htmx disables the
    // button in flight, and Chrome then moves its focus to the body at the
    // next frame.
    const active = document.activeElement;
    if (active === body.form.querySelector('button[type="submit"]:not([form])') || active === document.body) body.focus({ preventScroll: true });
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
    updateEdits();
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
