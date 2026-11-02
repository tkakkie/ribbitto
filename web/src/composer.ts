import * as dom from "./contracts.gen.ts";
import { createComposerQueue, type PendingItem, type Delivery } from "./composer-queue.ts";

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
  let rememberPost = () => {};
  let freshRestoredKey: string | null = null;
  let admitting: ComposerForm | undefined;
  const forms = new Map<PendingItem, ComposerForm>();
  const identities = new WeakMap<ComposerForm, PendingItem>();
  const projected = new WeakMap<PendingItem, PendingItem>();
  const queue = createComposerQueue(() => performance.now(), () => crypto.randomUUID ? crypto.randomUUID() : freshRestoredKey!, (settled, notice) => {
    for (const item of queue.items) if (!forms.has(item) && admitting) {
      forms.set(item, admitting);
      identities.set(admitting, item);
    }
    for (const [item, form] of forms) {
      const previous = projected.get(item);
      // Admission marks uncertainty before state; completion writes state first.
      const uncertainty = () => {
        if (item.unconfirmed) dom.writeAttribute(form, dom.UnconfirmedAttribute, "true");
        else form.removeAttribute(dom.UnconfirmedAttribute);
      };
      if (!previous && item.unconfirmed) uncertainty();
      if (previous?.state !== item.state) dom.writeAttribute(form, dom.StateAttribute, item.state);
      if (previous && (previous.unconfirmed !== item.unconfirmed || (settled === item && notice === "unconfirmed"))) uncertainty();
      if (item.postedMessage !== null && previous?.postedMessage !== item.postedMessage)
        dom.writeAttribute(form.closest("li")!, dom.PostedMessageAttribute, item.postedMessage);
      projected.set(item, { ...item });
    }
  });
  const releaseRestored = (body: ComposerBody, key: string | undefined) => {
    if (key === undefined) return;
    body.form.removeAttribute(dom.UnconfirmedKeyAttribute);
    body.form.elements.idempotency_key.value = key;
    freshRestoredKey = null;
  };
  const changedRestoration = (body: ComposerBody) => releaseRestored(body, queue.changeText(body.value));
  const place = (form: ComposerForm) => JSON.stringify([dom.readAttribute(form, dom.DraftChannelAttribute), dom.readAttribute(form, dom.DraftTopicAttribute) || "feed"]);
  window.addEventListener("pagehide", () => queue.pagehide());
  document.addEventListener(dom.MessageDraftRestoredEvent, (native) => {
    const event = native as CustomEvent<dom.MessageDraftRestoredDetail> & { target: ComposerBody };
    // The ordinary fallback still has the fresh key rendered by the server.
    freshRestoredKey = event.target.form.elements.idempotency_key.value;
    queue.restore(event.detail.key, event.target.value);
    event.target.form.elements.idempotency_key.value = event.detail.key;
    dom.writeAttribute(event.target.form, dom.UnconfirmedKeyAttribute, event.detail.key);
  });
  document.addEventListener(dom.MessageSentEvent, (native) => {
    const event = native as CustomEvent<dom.MessageSentDetail> & { target: ComposerBody };
    releaseRestored(event.target, queue.sent(event.detail.key!));
  });
  const updateEdits = () => {
    const body = (dom.getMessageBody() as ComposerBody);
    const occupied = body.value !== "" || composing.has(body);
    for (const form of pendingForms()) {
      const edit = form.querySelector<HTMLButtonElement>(`[${dom.PendingEditAttribute}]`)!;
      const help = form.querySelector<HTMLElement>(`[${dom.PendingEditHelpAttribute}]`)!;
      edit.disabled = occupied || identities.get(form)?.state !== "failed";
      help.hidden = !occupied;
      edit.setAttribute("aria-describedby", form.closest("li")!.querySelector(`[${dom.PendingBodyAttribute}]`)!.id + (occupied ? " " + help.id : ""));
    }
  };
  const pendingForms = () => [...document.querySelectorAll<ComposerForm>(`${dom.messagePendingSelector()} [${dom.PendingFormAttribute}]`)];
  const dispatch = () => {
    const head = queue.next((dom.getMessageComposer() as ComposerForm | null)?.classList.contains("htmx-request"));
    if (head) htmx.trigger(forms.get(head)!, dom.PendingSendEvent);
  };
  document.addEventListener("submit", (native) => {
    const event = native as SubmitEvent & { target: ComposerForm };
    if (event.target.matches(`[${dom.PendingFormAttribute}]`)) {
      event.preventDefault();
      event.stopImmediatePropagation();
      const form = event.target;
      const pending = identities.get(form)!;
      if (pending.state !== "failed") return;
      // A refused send will now be stored after the later sends. An unknown
      // outcome keeps its place until its same-key retry resolves it.
      if (!pending.unconfirmed) form.closest("li")!.parentElement!.append(form.closest("li")!);
      queue.retry(pending);
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
    const item = template.content.firstElementChild!.cloneNode(true) as HTMLLIElement;
    const sending = item.querySelector<ComposerForm>("form")!;
    admitting = sending;
    const pending = queue.admit(body.value, place(form), form.classList.contains("htmx-request"), Boolean(form.elements.quote));
    admitting = undefined;
    if (typeof pending === "string") {
      if (pending === "ordinary") inPlace();
      else { event.preventDefault(); event.stopImmediatePropagation(); say(pending); }
      return;
    }
    event.preventDefault();
    event.stopImmediatePropagation();
    for (const name of ["idempotency_key", "composer", dom.CursorField]) (sending.elements.namedItem(name) as HTMLInputElement).value = (form.elements.namedItem(name) as HTMLInputElement | null)?.value || "";
    sending.elements.idempotency_key.value = pending.requestKey;
    form.removeAttribute(dom.UnconfirmedKeyAttribute);
    sending.elements.body.value = body.value;
    const shownBody = item.querySelector(`[${dom.PendingBodyAttribute}]`)!;
    shownBody.textContent = pending.text.replace(/\r\n?/g, "\n").trim();
    shownBody.id += "-" + sending.elements.idempotency_key.value;
    sending.querySelector<HTMLElement>(`[${dom.PendingEditHelpAttribute}]`)!.id += "-" + sending.elements.idempotency_key.value;
    for (const button of sending.querySelectorAll("button")) button.setAttribute("aria-describedby", shownBody.id);
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
  const delivery = (): Delivery => ({
    ids: [...dom.getMessageItems()?.children || []].map(item => item.id),
    topic: dom.readAttribute(dom.getMessageItems()!, dom.TopicAttribute) || undefined,
  });
  const reconcile = () => {
    const list = dom.getMessagePending()!;
    if (list) queue.deliver(delivery());
    for (const form of pendingForms()) {
      const pending = identities.get(form);
      if (pending && !queue.items.includes(pending)) {
        form.closest("li")!.remove();
        if (pending.state !== "sending") forms.delete(pending);
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
    queue.deliver({ ...delivery(), moved: { fromTopic: dom.readAttribute(moved!, dom.FromTopicAttribute)!,
      ids: [...payload.querySelectorAll("[id]")].map(item => item.id) } });
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
    const pending = identities.get(form)!;
    if (edit) {
      const restored = queue.edit(pending, body.value, composing.has(body));
      if (!restored) return;
      body.value = restored.text;
      body.form.elements.idempotency_key.value = restored.requestKey;
      if (restored.unconfirmed) dom.writeAttribute(body.form, dom.UnconfirmedKeyAttribute, restored.requestKey);
      else body.form.removeAttribute(dom.UnconfirmedKeyAttribute);
      body.setSelectionRange(body.value.length, body.value.length);
    } else {
      queue.remove(pending, place(body.form));
      if (pending.unconfirmed) setTimeout(() => queue.expire(pending), queue.removedKeyRetention);
    }
    dom.dispatchMessagePendingActionEvent((dom.getMessageBody() as ComposerBody), { key: pending.draftKey, restoredKey: edit ? queue.restoration!.key : null, kind: edit ? "edit" : "remove" });
    if (pending.state !== "sending") forms.delete(pending);
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
    queue.dispatch();
    // htmx dispatches completion on the form even after it is detached,
    // when the event can no longer bubble to document.
    form.addEventListener("htmx:afterRequest", (answer) => {
      completePending(answer as HtmxEvent, form);
      const pending = identities.get(form)!;
      if (!queue.items.includes(pending)) forms.delete(pending);
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
    if (!pending && [200, 409].includes(event.detail.xhr.status)) queue.forgetRestoration();
    if (queue.restoration) body.form.elements.idempotency_key.value = queue.restoration.key;
    if (pending) {
      const item = identities.get(pending)!;
      const id = String(dom.readAttribute(state, dom.PostedMessageAttribute) ?? undefined);
      queue.response(item, id);
      const view = delivery();
      const topic = dom.readAttribute(state, dom.PostedTopicAttribute)!;
      const applied = view.topic && dom.readAttribute(dom.getOrganizationStream()!, dom.AppliedCursorAttribute);
      // Routing short-circuits cursor parsing, including on malformed values.
      const checkCursor = view.topic && topic === view.topic && !view.ids.includes(id) && applied;
      if (checkCursor) view.applied = BigInt(applied);
      queue.route(item, { id, topic, seq: checkCursor ? BigInt(dom.readAttribute(state, dom.PostedSeqAttribute)!) : 0n }, view);
      reconcile();
      if (event.detail.xhr.status === 200 && dom.readAttribute(state, dom.PostedMessageAttribute)) dom.dispatchMessageSentEvent((dom.getMessageBody() as ComposerBody), { key: item.draftKey, message: dom.readAttribute(state, dom.PostedMessageAttribute)! });
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
    const item = identities.get(pending)!;
    const name = queue.settle(item, status);
    if (status === 409) pending.elements.idempotency_key.value = item.requestKey;
    if (!name) return;
    const alert = dom.getMessageSendAlert()!;
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
    queue.ordinarySettled(status);
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
