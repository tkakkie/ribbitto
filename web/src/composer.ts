import * as dom from "./contracts.gen.ts";

// These controls belong to the server-rendered composer and pending template.
type ComposerForm = HTMLFormElement & { elements: HTMLFormControlsCollection &
  Record<typeof dom.CursorField | "idempotency_key" | "composer", HTMLInputElement> & { body: ComposerBody | HTMLInputElement; quote?: HTMLInputElement } };
type ComposerBody = HTMLTextAreaElement & { form: ComposerForm };
// htmx supplies detail; target types are narrowed by each delegated hook below.
type HtmxEvent<T extends HTMLElement = HTMLElement> = CustomEvent<{
  xhr: { status: number }; shouldSwap: boolean; type: string; data: string;
  requestConfig: { elt: ComposerForm; formData: FormData };
  triggeringEvent?: SubmitEvent; parameters: Record<string, string>;
}> & { target: T };
type AlertName = "unconfirmed" | "stillUnconfirmed" | "notSent" | "recovered" | "repeat" | "capacity" | "busy";
const alerts = { unconfirmed: dom.UnconfirmedAttribute, stillUnconfirmed: dom.StillUnconfirmedAttribute,
  notSent: dom.NotSentAttribute, recovered: dom.RecoveredAttribute, repeat: dom.RepeatAttribute,
  capacity: dom.CapacityAttribute, busy: dom.BusyAttribute } as const;

(() => {
  // Safari's keyboard shrinks/pans only the visual viewport, not dvh.
  const viewport = window.visualViewport;
  const updateViewport = () => {
    const style = dom.getOrganizationStream()?.style;
    if (!style) return;
    const differs = viewport && (Math.abs(viewport.height - window.innerHeight) > 1 || Math.abs(viewport.offsetTop) > 1);
    for (const [name, value] of [["--composer-viewport-h", viewport?.height], ["--composer-viewport-top", viewport?.offsetTop]] as const) {
      const next = differs ? value + "px" : "";
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
  const pendingRequests = new Set<ComposerForm>();
  let rememberPost = () => {};
  // An edited send keeps its identity even when another queue response swaps
  // the hidden controls. Only sending or emptying this text releases it.
  let restoredKey: string | null = null;
  let restoredText: string | null = null;
  let freshRestoredKey: string | null = null;
  const releaseRestored = (body: ComposerBody) => {
    restoredKey = null;
    restoredText = null;
    body.form.removeAttribute(dom.UnconfirmedKeyAttribute);
    body.form.elements.idempotency_key.value = crypto.randomUUID ? crypto.randomUUID() : freshRestoredKey!;
    freshRestoredKey = null;
  };
  const changedRestoration = (body: ComposerBody) => {
    if (restoredKey && (body.value === "" || (restoredText !== null && body.value !== restoredText))) releaseRestored(body);
  };
  // A deliberate Remove only protects an unchanged resend briefly (#1328).
  const removedKeyRetention = 5 * 60 * 1000;
  const removedKey = (form: ComposerForm, text: string) => JSON.stringify([dom.readAttribute(form, dom.DraftChannelAttribute), dom.readAttribute(form, dom.DraftTopicAttribute) || "feed", text]);
  const removedKeys = new Map<string, { key: string; at: number }>();
  window.addEventListener("pagehide", () => removedKeys.clear());
  document.addEventListener(dom.MessageDraftRestoredEvent, (native) => {
    const event = native as CustomEvent<dom.MessageDraftRestoredDetail> & { target: ComposerBody };
    // The ordinary fallback still has the fresh key rendered by the server.
    freshRestoredKey = event.target.form.elements.idempotency_key.value;
    restoredKey = event.detail.key;
    restoredText = event.target.value;
    event.target.form.elements.idempotency_key.value = restoredKey;
    dom.writeAttribute(event.target.form, dom.UnconfirmedKeyAttribute, restoredKey);
  });
  document.addEventListener(dom.MessageSentEvent, (native) => {
    const event = native as CustomEvent<dom.MessageSentDetail> & { target: ComposerBody };
    for (const [text, held] of removedKeys) {
      if (held.key === event.detail.key) removedKeys.delete(text);
    }
    if (restoredKey !== event.detail.key) return;
    releaseRestored(event.target);
  });
  const updateEdits = () => {
    const body = (dom.getMessageBody() as ComposerBody);
    const occupied = body.value !== "" || composing.has(body);
    for (const form of pendingForms()) {
      const edit = form.querySelector<HTMLButtonElement>(`[${dom.PendingEditAttribute}]`)!;
      const help = form.querySelector<HTMLElement>(`[${dom.PendingEditHelpAttribute}]`)!;
      edit.disabled = occupied || dom.readAttribute(form, dom.StateAttribute) !== "failed";
      help.hidden = !occupied;
      edit.setAttribute("aria-describedby", form.closest("li")!.querySelector(`[${dom.PendingBodyAttribute}]`)!.id + (occupied ? " " + help.id : ""));
    }
  };
  const pendingForms = () => [...document.querySelectorAll<ComposerForm>(`${dom.messagePendingSelector()} [${dom.PendingFormAttribute}]`)];
  const dispatch = () => {
    if (pendingRequests.size || (dom.getMessageComposer() as ComposerForm | null)?.classList.contains("htmx-request")) return;
    const forms = pendingForms();
    if (forms.some(form => dom.readAttribute(form, dom.StateAttribute) === "failed" && dom.readAttribute(form, dom.UnconfirmedAttribute))) return;
    const head = forms.find(form => dom.readAttribute(form, dom.StateAttribute) === "queued");
    if (head) htmx.trigger(head, dom.PendingSendEvent);
  };
  document.addEventListener("submit", (native) => {
    const event = native as SubmitEvent & { target: ComposerForm };
    if (event.target.matches(`[${dom.PendingFormAttribute}]`)) {
      event.preventDefault();
      event.stopImmediatePropagation();
      const form = event.target;
      if (dom.readAttribute(form, dom.StateAttribute) !== "failed") return;
      // A refused send will now be stored after the later sends. An unknown
      // outcome keeps its place until its same-key retry resolves it.
      if (!dom.readAttribute(form, dom.UnconfirmedAttribute)) form.closest("li")!.parentElement!.append(form.closest("li")!);
      dom.writeAttribute(form, dom.StateAttribute, "queued");
      form.querySelector("[role=status]")!.textContent = "";
      form.querySelector<HTMLElement>(`[${dom.PendingActionsAttribute}]`)!.hidden = true;
      form.closest("li")!.querySelector<HTMLElement>(`[${dom.PendingSendingAttribute}]`)!.hidden = false;
      (dom.getMessageBody() as ComposerBody).focus({ preventScroll: true });
      dispatch();
      return;
    }
    if (event.target.id !== dom.MessageComposerID) return;
    // Quote shares the composer's htmx lock; guard the pending/plain paths too,
    // before either can submit stale quote state or clear the draft.
    if (dom.getQuoteForm()?.classList.contains("htmx-request")) {
      event.preventDefault();
      event.stopImmediatePropagation();
      return;
    }
    const stream = dom.getOrganizationStream();
    const cursor = dom.getCursorField(event.target);
    if (stream && cursor) cursor.value = dom.readAttribute(stream, dom.EventCursorAttribute)!;
    const template = dom.getMessagePendingTemplate();
    changedRestoration(event.target.elements.body as ComposerBody);
    const inPlace = () => dom.dispatchMessageSendingEvent((dom.getMessageBody() as ComposerBody), { key: event.target.elements.idempotency_key?.value ?? null, body: event.target.elements.body.value, inPlace: true });
    if (!template || !crypto.randomUUID || !event.target.elements.idempotency_key || !cursor) {
      if (!event.target.classList.contains("htmx-request")) inPlace();
      return;
    }
    const form = event.target, body = form.elements.body as ComposerBody;
    const list = dom.getMessagePending()!;
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
    const item = template.content.firstElementChild!.cloneNode(true) as HTMLLIElement;
    const sending = item.querySelector<ComposerForm>("form")!;
    for (const name of ["idempotency_key", "composer", dom.CursorField]) (sending.elements.namedItem(name) as HTMLInputElement).value = (form.elements.namedItem(name) as HTMLInputElement | null)?.value || "";
    const removed = removedKeys.get(removedKey(form, body.value));
    removedKeys.delete(removedKey(form, body.value));
    sending.elements.idempotency_key.value = restoredKey ||
      (removed && performance.now() - removed.at < removedKeyRetention ? removed.key : crypto.randomUUID());
    if (removed && sending.elements.idempotency_key.value === removed.key) dom.writeAttribute(sending, dom.UnconfirmedAttribute, "true");
    if (restoredKey && dom.readAttribute(form, dom.UnconfirmedKeyAttribute) === restoredKey) dom.writeAttribute(sending, dom.UnconfirmedAttribute, "true");
    form.removeAttribute(dom.UnconfirmedKeyAttribute);
    restoredKey = null;
    restoredText = null;
    // Correlate draft cleanup even after a 409 renews the request key.
    dom.writeAttribute(sending, dom.DraftKeyAttribute, sending.elements.idempotency_key.value);
    sending.elements.body.value = body.value;
    const shownBody = item.querySelector(`[${dom.PendingBodyAttribute}]`)!;
    shownBody.textContent = text;
    shownBody.id += "-" + sending.elements.idempotency_key.value;
    sending.querySelector<HTMLElement>(`[${dom.PendingEditHelpAttribute}]`)!.id += "-" + sending.elements.idempotency_key.value;
    for (const button of sending.querySelectorAll("button")) button.setAttribute("aria-describedby", shownBody.id);
    dom.writeAttribute(sending, dom.StateAttribute, "queued");
    list.append(item);
    dom.dispatchMessageSendingEvent((dom.getMessageBody() as ComposerBody), { key: sending.elements.idempotency_key.value, body: body.value, inPlace: false });
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
  const pendingForm = (event: HtmxEvent) => {
    const form = event.detail.requestConfig?.elt;
    return form?.matches(`[${dom.PendingFormAttribute}]`) ? form : null;
  };
  // Mutation observers run before paint: a response-first delivery never
  // paints both identities. Stream-first waits for the response's ID.
  const reconcile = () => {
    const list = dom.getMessagePending()!;
    for (const item of [...(list?.children || [])] as HTMLLIElement[]) {
      if (dom.readAttribute(item, dom.PostedMessageAttribute) && document.getElementById(dom.readAttribute(item, dom.PostedMessageAttribute)!)?.parentElement!.id === dom.MessageItemsID) {
        item.remove();
        say("");
      }
    }
    const empty = dom.getMessageEmpty();
    if (empty) empty.style.display = list?.firstElementChild ? "none" : "";
  };
  const items = dom.getMessageItems()!;
  if (items) new MutationObserver(reconcile).observe(items, { childList: true });
  document.addEventListener("htmx:sseBeforeMessage", (native) => {
    const event = native as HtmxEvent;
    if (event.target.id !== dom.MessageItemsID || event.detail.type !== "messages-moved") return;
    if (!dom.readAttribute(items, dom.TopicAttribute)) return;
    const payload = new DOMParser().parseFromString(event.detail.data, "text/html");
    const moved = payload.querySelector("ul");
    if ((moved && dom.readAttribute(moved, dom.FromTopicAttribute)) !== dom.readAttribute(items, dom.TopicAttribute)) return;
    for (const form of pendingForms()) {
      const item = form.closest("li")!;
      if (dom.readAttribute(item, dom.PostedMessageAttribute) && payload.getElementById(dom.readAttribute(item, dom.PostedMessageAttribute)!)) {
        item.remove();
        say("");
      }
    }
    reconcile();
  });
  document.addEventListener("htmx:configRequest", (native) => {
    const event = native as HtmxEvent;
    if (event.target.id !== dom.QuoteFormID) return;
    // Closing the popover restores focus, clearing htmx's remembered button.
    // The native submit event retains it, including for live-delivered menus.
    const button = event.detail.triggeringEvent?.submitter as HTMLButtonElement | null | undefined;
    if (button?.name) event.detail.parameters[button.name] = button.value;
  });
  document.addEventListener("click", (native) => {
    const event = native as PointerEvent & { target: HTMLElement };
    event.target.closest(`[form="${dom.QuoteFormID}"]`)?.closest<HTMLElement>("[popover]")?.hidePopover();
    const edit = event.target.closest(`[${dom.PendingEditAttribute}]`);
    if (!edit && !event.target.closest(`[${dom.PendingRemoveAttribute}]`)) return;
    const body = (dom.getMessageBody() as ComposerBody);
    const form = event.target.closest<ComposerForm>("form")!;
    if (edit) {
      if (body.value || composing.has(body) || dom.readAttribute(form, dom.StateAttribute) !== "failed") return;
      body.value = form.elements.body.value;
      restoredKey = form.elements.idempotency_key.value;
      restoredText = null;
      body.form.elements.idempotency_key.value = restoredKey;
      if (dom.readAttribute(form, dom.UnconfirmedAttribute)) dom.writeAttribute(body.form, dom.UnconfirmedKeyAttribute, restoredKey);
      else body.form.removeAttribute(dom.UnconfirmedKeyAttribute);
      body.setSelectionRange(body.value.length, body.value.length);
    } else if (dom.readAttribute(form, dom.UnconfirmedAttribute)) {
      for (const [text, held] of removedKeys) {
        if (performance.now() - held.at >= removedKeyRetention) removedKeys.delete(text);
      }
      const text = removedKey(body.form, form.elements.body.value);
      const held = { key: form.elements.idempotency_key.value, at: performance.now() };
      removedKeys.set(text, held);
      setTimeout(() => { if (removedKeys.get(text) === held) removedKeys.delete(text); }, removedKeyRetention);
    }
    dom.dispatchMessagePendingActionEvent((dom.getMessageBody() as ComposerBody), { key: dom.readAttribute(form, dom.DraftKeyAttribute)!, restoredKey: edit ? restoredKey : null, kind: edit ? "edit" : "remove" });
    event.target.closest("li")!.remove();
    updateEdits();
    say("");
    reconcile();
    dispatch();
    (dom.getMessageBody() as ComposerBody).focus({ preventScroll: true });
  });
  document.addEventListener("htmx:beforeRequest", (native) => {
    const event = native as HtmxEvent;
    const form = pendingForm(event);
    if (!form) return;
    pendingRequests.add(form);
    dom.writeAttribute(form, dom.StateAttribute, "sending");
    // htmx dispatches completion on the form even after it is detached,
    // when the event can no longer bubble to document.
    form.addEventListener("htmx:afterRequest", (answer) => {
      completePending(answer as HtmxEvent, form);
      pendingRequests.delete(form);
      // Finish htmx's request cleanup before triggering the next form.
      queueMicrotask(dispatch);
    }, { once: true });
    (dom.getMessageBody() as ComposerBody).focus({ preventScroll: true });
    form.querySelector("[role=status]")!.textContent = "";
    form.querySelector<HTMLElement>(`[${dom.PendingActionsAttribute}]`)!.hidden = true;
    form.closest("li")!.querySelector<HTMLElement>(`[${dom.PendingSendingAttribute}]`)!.hidden = false;
  });
  document.addEventListener("htmx:beforeSwap", (native) => {
    const event = native as HtmxEvent;
    if (pendingForm(event) && event.detail.xhr.status !== 200) event.detail.shouldSwap = false;
  });
  // Some phone IMEs end composition before dispatching the confirming Enter,
  // with neither isComposing nor 229. Keep that short event sequence local to
  // its textarea; a later deliberate key follows the selected send mode.
  const compositionEnded = new WeakMap<EventTarget, number>();
  // A post's success may arrive mid-composition, when rewriting the value
  // would break the composition: the sent text then waits for its end.
  const composing = new WeakSet<ComposerBody>();
  const pendingRemoval = new WeakMap<ComposerBody, () => void>();
  // Only the sent text leaves, and only if the textarea still starts with it.
  // setRangeText keeps the caret and selection on the remaining text and
  // fires no input event, so no typing signal follows.
  const removeSent = (body: ComposerBody, sent: string | null | undefined) => {
    if (!sent || !body.value.startsWith(sent)) return false;
    body.setRangeText("", 0, sent.length, "preserve");
    return true;
  };
  const afterComposition = (body: ComposerBody, removal: () => void) => {
    if (composing.has(body)) pendingRemoval.set(body, removal);
    else removal();
  };
  // The alert's texts are server-rendered and localised; an empty name clears it.
  const say = (name: AlertName | "") => {
    const alert = dom.getMessageSendAlert();
    if (alert) alert.textContent = name ? dom.readAttribute(alert, alerts[name]) : "";
  };
  document.addEventListener("compositionstart", (native) => {
    const event = native as CompositionEvent & { target: ComposerBody };
    if (event.target.id !== dom.MessageBodyID) return;
    composing.add(event.target);
    updateEdits();
  });
  document.addEventListener("compositionend", (native) => {
    const event = native as CompositionEvent & { target: ComposerBody };
    const body = event.target;
    if (body.id !== dom.MessageBodyID) return;
    compositionEnded.set(body, event.timeStamp);
    composing.delete(body);
    if (pendingRemoval.has(body)) {
      pendingRemoval.get(body)!();
      pendingRemoval.delete(body);
    }
    updateEdits();
  });
  const command = (event: KeyboardEvent & { target: ComposerBody }) => {
    if (event.isComposing || event.keyCode === 229 || event.key !== "Enter") return;
    const ended = compositionEnded.get(event.target);
    if (ended !== undefined && event.timeStamp - ended < 100) return;
    const mode = dom.readAttribute(dom.getMessageComposerState()!, dom.SendKeyAttribute);
    return event.shiftKey || (mode === "modifier_enter" && !event.ctrlKey && !event.metaKey) ? "newline" : "send";
  };
  const updateEnterHint = () => {
    const state = dom.getMessageComposerState()!;
    const body = (dom.getMessageBody() as ComposerBody);
    if (state && body) body.setAttribute("enterkeyhint", dom.readAttribute(state, dom.SendKeyAttribute) === "modifier_enter" ? "enter" : "send");
  };
  updateEnterHint();
  const submit = (form: ComposerForm) => form.requestSubmit();
  document.addEventListener("keydown", (native) => {
    const event = native as KeyboardEvent & { target: ComposerBody };
    if (event.target.id !== dom.MessageBodyID || command(event) !== "send") return;
    event.preventDefault();
    if (!event.repeat && !event.target.disabled) submit(event.target.form);
  });
  const signal = (native: Event) => {
    const event = native as InputEvent & { target: ComposerBody };
    if (event.target.id !== dom.MessageBodyID || event.isComposing) return;
    const form = dom.getTypingForm();
    if (form && event.target.value.trim()) htmx.trigger(form, dom.TypingInputEvent);
  };
  // Native caret scrolling can precede field-sizing layout, including a
  // select-all replacement at the cap. Reveal the end using the final size.
  const revealEnd = (body: ComposerBody) => {
    if (document.activeElement === body && body.selectionStart === body.value.length && body.selectionEnd === body.value.length) {
      body.scrollTop = body.scrollHeight;
    }
  };
  const body = (dom.getMessageBody() as ComposerBody);
  let quoteResized = false;
  const updateQuoteHeight = () => {
    if (!body) return;
    const quote = dom.getComposerQuote();
    const height = quote?.getBoundingClientRect().height || 0;
    // A visible chip adds one flex gap as well as its own height.
    const next = (height ? height + parseFloat(getComputedStyle(body.form).rowGap) : 0) + "px";
    // The CSS fallback already covers an absent chip, without invalidation.
    if ((body.form.style.getPropertyValue("--composer-quote-h") || "0px") === next) return;
    body.form.style.setProperty("--composer-quote-h", next);
    // Measuring a removed chip can briefly grow the list and clamp its scroll,
    // even when the updated cap brings the list back to the same height.
    quoteResized = true;
  };
  document.addEventListener("input", (native) => {
    const event = native as InputEvent & { target: ComposerBody };
    if (event.target.id !== dom.MessageBodyID) return;
    requestAnimationFrame(() => revealEnd(event.target));
    changedRestoration(event.target);
    updateEdits();
  });
  document.addEventListener("input", signal);
  document.addEventListener("compositionend", signal);
  // A message's URL (decision 36) lands on the message its fragment names,
  // which the browser has brought into view; only other loads start at the
  // newest entry.
  const newest = () => {
    const list = dom.getMessageList();
    const target = document.getElementById(location.hash.slice(1));
    if (target && target.parentElement?.id === dom.MessageItemsID) return;
    if (list) list.scrollTop = list.scrollHeight;
  };
  const pane = dom.getMessageList();
  if (pane) {
    let height: number, position: { newest: boolean; item?: HTMLLIElement; offset: number }, measured = false, settling = false;
    const remember = () => {
      const top = pane.getBoundingClientRect().top;
      let item: HTMLLIElement | undefined;
      for (const candidate of pane.querySelectorAll<HTMLLIElement>(`${dom.messageItemsSelector()} > li, ${dom.messagePendingSelector()} > li`)) {
        if (candidate.getBoundingClientRect().bottom > top) { item = candidate; break; }
      }
      position = { newest: pane.scrollHeight - pane.scrollTop - pane.clientHeight <= 2,
        item, offset: item ? item.getBoundingClientRect().top - top : 0 };
    };
    rememberPost = () => {
      // A background-tab settle can precede the initial observer delivery.
      if (!measured) return;
      height = pane.clientHeight;
      remember();
    };
    // A resize can clamp scrollTop before its scroll event. Keep the state
    // from the old height until the observer has restored the reader.
    pane.addEventListener("scroll", () => { if (measured && !quoteResized && pane.clientHeight === height) remember(); });
    const resize = new ResizeObserver(() => {
      if (!measured) {
        measured = true;
        // The observer supplies initial layout before paint. Save newest (or
        // the native fragment position) before writing the quote cap, so no
        // read forces its layout. The deeper textarea/form observations let
        // a cap resize deliver again and restore that intent before paint.
        newest();
        rememberPost();
        if (body) revealEnd(body);
        updateQuoteHeight();
        quoteResized = false;
        return;
      }
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
    document.addEventListener("htmx:afterSwap", (native) => {
      const event = native as HtmxEvent;
      if (event.target.id === dom.MessageComposerStateID && dom.readAttribute(event.target, dom.PostedMessageAttribute)) settling = true;
    });
    document.addEventListener("htmx:afterSettle", (native) => {
      const event = native as HtmxEvent;
      // History restores and the stream's afterSettleCallback scroll run
      // synchronously after this listener. Save their result before resizing.
      if (pane.contains(event.target)) queueMicrotask(rememberPost);
      if (event.target.id !== dom.MessageComposerStateID || !settling) return;
      // The stream's scrollPosted runs in this same settle event. Its scroll
      // wins even when clearing the sent text also resizes the pane.
      queueMicrotask(() => { rememberPost(); settling = false; });
    });
  }
  const quoteFocus = new WeakMap<HtmxEvent["detail"]["xhr"], { selector: string; start: number | null; end: number | null; direction: "forward" | "backward" | "none" | null }>();
  document.addEventListener("htmx:beforeSwap", (native) => {
    const event = native as HtmxEvent;
    if (![422, 409].includes(event.detail.xhr.status)) return;
    const active = document.activeElement as HTMLTextAreaElement;
    if (!active.closest(dom.composerQuoteSelector())) return;
    const selector = active.id === dom.QuoteTextID ? dom.quoteTextSelector() :
      active.name === "part" ? 'button[name="part"]' : "button:not([name])";
    quoteFocus.set(event.detail.xhr, { selector, start: active.selectionStart,
      end: active.selectionEnd, direction: active.selectionDirection });
  });
  // Responses replace only #message-composer-state (#1173): the textarea
  // stays enabled and in place while a post is in flight.
  document.addEventListener("htmx:afterSwap", (native) => {
    const event = native as HtmxEvent;
    const state = event.target;
    if (state.id === dom.ComposerQuoteID) {
      updateQuoteHeight();
      const text = dom.getQuoteText(state);
      // Keep server autofocus for native no-script navigation. htmx replays
      // it at settle, so swapped chips leave focus entirely to this handler.
      text?.removeAttribute("autofocus");
      if ([422, 409].includes(event.detail.xhr.status)) {
        const saved = quoteFocus.get(event.detail.xhr);
        const replacement = saved && state.querySelector<HTMLElement>(saved.selector);
        if (replacement) {
          replacement.focus({ preventScroll: true });
          if (replacement === text) text!.setSelectionRange(saved.start!, saved.end!, saved.direction!);
        }
      } else {
        (text || state.querySelector<HTMLButtonElement>("button") || (dom.getMessageBody() as ComposerBody)).focus({ preventScroll: true });
      }
      return;
    }
    if (state.id !== dom.MessageComposerStateID) return;
    updateEnterHint();
    const body = (dom.getMessageBody() as ComposerBody);
    body.setAttribute("aria-invalid", String(state.hasAttribute(dom.InvalidAttribute)));
    const pending = pendingForm(event);
    // Guard against a future ordinary-send path retaining an edited key.
    if (!pending && [200, 409].includes(event.detail.xhr.status)) restoredKey = null;
    if (restoredKey) body.form.elements.idempotency_key.value = restoredKey;
    if (pending) {
      const item = pending.closest("li")!;
      dom.writeAttribute(item, dom.PostedMessageAttribute, String(dom.readAttribute(state, dom.PostedMessageAttribute) ?? undefined));
      // Read response routing once; retain only the confirmed ID on the item.
      // Ordered application proves an absent post has already moved away.
      const topic = dom.readAttribute(items, dom.TopicAttribute);
      if (topic) {
        const applied = dom.readAttribute(dom.getOrganizationStream()!, dom.AppliedCursorAttribute);
        if (dom.readAttribute(state, dom.PostedTopicAttribute) !== topic ||
            (!items.querySelector("#" + dom.readAttribute(state, dom.PostedMessageAttribute)) && applied &&
             BigInt(applied) >= BigInt(dom.readAttribute(state, dom.PostedSeqAttribute)!))) {
          item.remove();
          say("");
        }
      }
      reconcile();
      if (event.detail.xhr.status === 200 && dom.readAttribute(state, dom.PostedMessageAttribute)) dom.dispatchMessageSentEvent((dom.getMessageBody() as ComposerBody), { key: dom.readAttribute(pending, dom.DraftKeyAttribute), message: dom.readAttribute(state, dom.PostedMessageAttribute)! });
      return;
    }
    // The stream consumes this attribute at settle, possibly before IME ends.
    const message = dom.readAttribute(state, dom.PostedMessageAttribute);
    const sentEvent = () => { if (message) dom.dispatchMessageSentEvent((dom.getMessageBody() as ComposerBody), { key: event.detail.requestConfig.formData.get("idempotency_key") as string | null, message }); };
    const status = event.detail.xhr.status;
    if (status === 200) {
      const sent = event.detail.requestConfig.formData.get("body") as string | null;
      say("");
      afterComposition(body, () => { removeSent(body, sent); updateEdits(); sentEvent(); });
    }
    // A 409 names the stored message for this key. The server sends its body
    // only when the retried text started with it: the earlier answer was
    // lost while the member typed on (#1179). If the start has been edited
    // since, the 409's error, which the help then omits, is said here.
    if (status === 409) {
      const sent = dom.readAttribute(state, dom.SentBodyAttribute);
      say("");
      afterComposition(body, () => {
        if (sent) {
          const recovered = removeSent(body, sent);
          say(recovered ? "recovered" : "repeat");
          if (recovered) sentEvent();
        }
      });
    }
    // Focus on the send button returns to the textarea. htmx disables the
    // button in flight, and Chrome then moves its focus to the body at the
    // next frame.
    const active = document.activeElement;
    if (active === body.form.querySelector('button[type="submit"]:not([form])') || active === document.body) body.focus({ preventScroll: true });
  });
  const completePending = (event: HtmxEvent, pending: ComposerForm) => {
    const status = event.detail.xhr.status;
    if (status === 200) {
      dom.writeAttribute(pending, dom.StateAttribute, "confirmed");
      pending.removeAttribute(dom.UnconfirmedAttribute);
      return;
    }
    dom.writeAttribute(pending, dom.StateAttribute, "failed");
    const alert = dom.getMessageSendAlert()!;
    let name: AlertName = "unconfirmed";
    if (status === 409) {
      name = "repeat";
      pending.removeAttribute(dom.UnconfirmedAttribute);
      pending.elements.idempotency_key.value = crypto.randomUUID();
    } else if ([400, 404, 413, 422, 429].includes(status)) {
      name = dom.readAttribute(pending, dom.UnconfirmedAttribute) ? "stillUnconfirmed" : "notSent";
    } else dom.writeAttribute(pending, dom.UnconfirmedAttribute, "true");
    pending.querySelector("[role=status]")!.textContent = dom.readAttribute(alert, alerts[name]);
    pending.querySelector<HTMLElement>(`[${dom.PendingActionsAttribute}]`)!.hidden = false;
    pending.closest("li")!.querySelector<HTMLElement>(`[${dom.PendingSendingAttribute}]`)!.hidden = true;
    updateEdits();
  };
  // Every failure keeps the text and the key, so sending again repeats this
  // send (decision 37 (b)). Only a 200 or a 409 comes after the server's key
  // lookup and brings a new key; until then an unconfirmed send stays marked
  // on its key, whatever a retry is answered (#1179).
  document.addEventListener("htmx:afterRequest", (native) => {
    const event = native as HtmxEvent<ComposerForm>;
    const form = event.target;
    if (form.id !== dom.MessageComposerID) return;
    const status = event.detail.xhr.status;
    const key = form.elements.idempotency_key?.value;
    if (dom.readAttribute(form, dom.UnconfirmedKeyAttribute) !== key) form.removeAttribute(dom.UnconfirmedKeyAttribute);
    if (status === 200 || status === 409) return;
    // The post handler answers 400/404/413/422 before any write; 429 rejects
    // the request by spec. Other 4xx, 5xx, network errors or timeouts may
    // follow a commit.
    if (status === 400 || status === 404 || status === 413 || status === 422 || status === 429) {
      say(dom.readAttribute(form, dom.UnconfirmedKeyAttribute) ? "stillUnconfirmed" : "notSent");
    } else {
      dom.writeAttribute(form, dom.UnconfirmedKeyAttribute, key);
      say("unconfirmed");
    }
  });
})();
