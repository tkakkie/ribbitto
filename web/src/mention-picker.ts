import {
  CountAttribute, getMentionListbox, getMentionPicker, getMentionQueryField, getMentionSearch,
  getMentionStatus, getMessageBody, HandleAttribute, MentionListboxID, MentionQueryEvent,
  MessageComposerID, QueryAttribute, readAttribute,
} from "./contracts.gen.ts";
import {
  activateMentionOptionIndex, canPickMentionCandidate, canShowMentionCandidates, classifyMentionKey,
  createMentionPickerState, cycleMentionOptionIndex, detectMentionToken, dismissMentionToken,
  endMentionComposition, formatMentionPick, resetMentionPicker, startMentionComposition, stepMentionQuery,
} from "./mention-token.ts";

type HtmxSwapEvent = CustomEvent<{ target?: HTMLElement | null }>;

(() => {
  const body = getMessageBody();
  const picker = getMentionPicker();
  const search = getMentionSearch();
  const status = getMentionStatus();
  if (!body || !picker || !search) return;
  const state = createMentionPickerState();
  const list = () => getMentionListbox()!;
  const options = () => [...list().querySelectorAll<HTMLLIElement>('[role="option"]')];
  const token = () => {
    if (state.composing || body.selectionStart !== body.selectionEnd) return null;
    return detectMentionToken(body.value.slice(0, body.selectionStart));
  };
  const close = () => {
    picker.hidden = true;
    body.removeAttribute("aria-activedescendant");
    activateMentionOptionIndex(state, -1, 0);
    options().forEach(option => option.setAttribute("aria-selected", "false"));
    status!.textContent = "";
  };
  const activate = (index: number) => {
    const items = options();
    const active = activateMentionOptionIndex(state, index, items.length);
    items.forEach((option, i) => option.setAttribute("aria-selected", String(i === index)));
    if (active >= 0) {
      body.setAttribute("aria-activedescendant", items[active].id);
      items[active].scrollIntoView({ block: "nearest" });
    }
  };
  const show = () => {
    const current = token();
    if (!canShowMentionCandidates(state, current, () => document.activeElement === body, () => readAttribute(list(), QueryAttribute))) {
      close();
      return;
    }
    picker.hidden = !options().length;
    status!.textContent = (readAttribute(list(), CountAttribute) ?? undefined) as unknown as string;
    activate(0);
  };
  const update = () => {
    const current = token();
    const step = stepMentionQuery(state, current);
    if (step === "unchanged") return;
    close();
    if (step === "closed") return;
    getMentionQueryField(search).value = current!.value;
    htmx.trigger(search, MentionQueryEvent);
  };
  const pick = (option: HTMLElement | null) => {
    const current = token();
    if (!canPickMentionCandidate(Boolean(option), current, () => picker.hidden, () => readAttribute(list(), QueryAttribute))) {
      close();
      return;
    }
    body.setRangeText(formatMentionPick(readAttribute(option!, HandleAttribute)), current.start, body.selectionStart, "end");
    close();
    body.dispatchEvent(new Event("input", { bubbles: true }));
  };
  body.addEventListener("compositionstart", () => { startMentionComposition(state); close(); });
  body.addEventListener("compositionend", (event) => {
    endMentionComposition(state, () => event.timeStamp);
    update();
  });
  body.addEventListener("input", (native) => {
    const event = native as InputEvent;
    if (event.isComposing) { startMentionComposition(state); close(); }
    if (!state.composing) update();
  });
  document.addEventListener("selectionchange", () => { if (document.activeElement === body) update(); });
  body.addEventListener("blur", () => { resetMentionPicker(state); close(); });
  body.addEventListener("focus", update);
  // The composer clears the textarea on send without an input event, so the
  // remembered query would make the same "@name" typed next a no-op.
  document.addEventListener("submit", (event) => {
    if ((event.target as HTMLElement).id !== MessageComposerID) return;
    resetMentionPicker(state);
    close();
  }, true);
  document.addEventListener("htmx:beforeSwap", (native) => {
    const event = native as HtmxSwapEvent;
    if (event.detail.target?.id === MentionListboxID) close();
  });
  document.addEventListener("htmx:afterSwap", (native) => {
    const event = native as HtmxSwapEvent;
    if (event.detail.target?.id !== MentionListboxID) return;
    state.ready = true;
    show();
  });
  // Capture runs before the composer's bubbling send handler, regardless of
  // script order. The IME commit Enter shares its 100 ms grace period.
  document.addEventListener("keydown", (event) => {
    if (event.target !== body) return;
    const action = classifyMentionKey(event, state, () => picker.hidden, () => {
      event.preventDefault();
      event.stopImmediatePropagation();
    });
    if (action === "ignore" || action === "consume") return;
    if (action === "close") { close(); return; }
    if (action === "dismiss") { dismissMentionToken(state, token()); close(); return; }
    if (action === "pick") {
      pick(options()[state.activeIndex] ?? null);
      return;
    }
    const items = options();
    const next = cycleMentionOptionIndex(state.activeIndex, items.length, () => event.key);
    if (next !== null) activate(next);
  }, true);
  for (const type of ["pointerdown", "mousedown"] as const) {
    picker.addEventListener(type, (event) => event.preventDefault());
  }
  picker.addEventListener("click", (event) => {
    if (!state.composing) pick((event.target as Element).closest<HTMLElement>('[role="option"]'));
  });
})();
