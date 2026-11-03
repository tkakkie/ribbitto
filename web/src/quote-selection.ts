import { QuoteFormID, QuoteTextField, quoteFormSelector } from "./contracts.gen.ts";
import {
  isSelectionSubmitter,
  quoteSelectionButtons,
  type SelectionQuoteState,
  updateSelectionQuote,
} from "./selection-quote.ts";

type HtmxConfigRequestEvent = CustomEvent<{
  triggeringEvent?: SubmitEvent;
  parameters: Record<string, string>;
}>;

(() => {
  const quoteTextInputSelector = `input[name="${QuoteTextField}"]`;
  const quoteFormInputSelector = `${quoteFormSelector()} ${quoteTextInputSelector}`;
  const state: SelectionQuoteState<Element> = { current: null, text: "" };

  function update(): void {
    updateSelectionQuote(state, document.getSelection());
    for (const button of document.querySelectorAll<HTMLButtonElement>(quoteSelectionButtons)) {
      button.hidden = !isSelectionSubmitter(button, state.current);
    }
  }
  function clear(): void {
    const input = document.querySelector<HTMLInputElement>(quoteFormInputSelector);
    if (input) {
      input.disabled = true;
      input.value = "";
    }
  }
  document.addEventListener("selectionchange", update);
  document.addEventListener("htmx:afterSwap", update);
  function press(event: Event): void {
    if (!(event.target as { closest?: (selector: string) => Element | null }).closest?.(quoteSelectionButtons)) return;
    // Read before the default focus/selection action. Cancelling pointerdown
    // and mousedown retains the Range without replacing native click submit.
    update();
    event.preventDefault();
  }
  document.addEventListener("pointerdown", press);
  document.addEventListener("mousedown", press);
  document.addEventListener("submit", (event: SubmitEvent) => {
    clear();
    if (!event.submitter?.matches(quoteSelectionButtons)) return;
    update();
    if (!isSelectionSubmitter(event.submitter, state.current)) {
      event.preventDefault();
      event.stopImmediatePropagation();
    }
  }, true);
  document.addEventListener("htmx:configRequest", (native: Event) => {
    const event = native as HtmxConfigRequestEvent;
    const target = event.target as Element;
    if (target.id !== QuoteFormID) return;
    clear();
    delete event.detail.parameters[QuoteTextField];
    const button = event.detail.triggeringEvent?.submitter;
    if (!button?.matches(quoteSelectionButtons)) return;
    // Always re-read: keyboard/programmatic submits need no pointer press,
    // and a changed or lost selection must never become a whole quote.
    update();
    if (!isSelectionSubmitter(button, state.current)) {
      event.preventDefault();
      return;
    }
    const input = target.querySelector<HTMLInputElement>(quoteTextInputSelector)!;
    input.value = state.text!;
    input.disabled = false;
    // htmx has collected controls already; its parameter proxy replaces any
    // previous value, retaining exactly one field in this request body.
    event.detail.parameters[QuoteTextField] = input.value;
  });
  for (const name of ["htmx:beforeSend", "htmx:afterRequest", "htmx:afterSwap"]) {
    document.addEventListener(name, clear);
  }
})();
