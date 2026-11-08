import {
  BodyMessageDOMPrefix,
  QuoteFormID,
  QuoteHandleAttribute,
  QuoteSelectionAttribute,
  QuoteTextField,
  quoteFormSelector,
  readAttribute,
} from "./contracts.gen.ts";
import {
  type ClonedSelection,
  isQuotableText,
  isSelectionSubmitter,
  type QuoteNode,
  type SelectionQuoteState,
  updateSelectionQuote,
} from "./selection-quote.ts";

type HtmxConfigRequestEvent = CustomEvent<{
  triggeringEvent?: SubmitEvent;
  parameters: Record<string, string>;
}>;

(() => {
  const bodySelector = `[id^="${BodyMessageDOMPrefix}"]`;
  const quoteHandleSelector = `[${QuoteHandleAttribute}]`;
  const quoteSelectionButtons = `button[${QuoteSelectionAttribute}]`;
  const quoteTextInputSelector = `input[name="${QuoteTextField}"]`;
  const quoteFormInputSelector = `${quoteFormSelector()} ${quoteTextInputSelector}`;
  const state: SelectionQuoteState<Element> = { current: null, text: "" };

  function bodyOf(node: Node | null | undefined): Element | null | undefined {
    return (node?.nodeType === Node.ELEMENT_NODE ? (node as Element) : node?.parentElement)?.closest(bodySelector);
  }
  function bothEnds(body: Element | null | undefined, selection: Selection): body is Element {
    if (!body) return false;
    const range = selection.getRangeAt(0);
    if (!body.contains(range.startContainer)) return false;
    if (body.contains(range.endContainer)) return true;
    // Triple-click may end at offset zero of the following element. Accept
    // only an empty tail, never text from another message or quote snapshot.
    const tail = range.cloneRange();
    tail.setStart(body, body.childNodes.length);
    return tail.cloneContents().textContent === "";
  }
  function nonempty(selection: Selection | null | undefined): selection is Selection {
    return !!selection && !selection.isCollapsed && isQuotableText(selection.toString());
  }
  function excludesQuotes(body: Element, selection: Selection): boolean {
    // Rich bodies may themselves contain blockquotes. A range crossing one
    // is excluded even when neither endpoint is inside the quote.
    return [...body.querySelectorAll("blockquote")].every(
      (quote) => !selection.getRangeAt(0).intersectsNode(quote),
    );
  }
  function selected(selection: Selection | null | undefined): Element | null {
    if (!nonempty(selection) || selection.rangeCount !== 1) return null;
    const body = bodyOf(selection.getRangeAt(0).startContainer);
    return bothEnds(body, selection) && excludesQuotes(body, selection) ? body : null;
  }
  function mentionAt(node: Node): Element | null | undefined {
    return (node.nodeType === Node.ELEMENT_NODE ? (node as Element) : node.parentElement)?.closest(
      quoteHandleSelector,
    );
  }
  function describeNode(node: Node): QuoteNode {
    if (node.nodeType === Node.TEXT_NODE) return { kind: "text", text: node.textContent ?? "" };
    if (node.nodeType !== Node.ELEMENT_NODE) return { kind: "other" };
    const element = node as Element;
    if (element.hasAttribute(QuoteHandleAttribute)) {
      return {
        kind: "element",
        tag: element.tagName,
        handle: readAttribute(element, QuoteHandleAttribute) ?? "",
      };
    }
    return {
      kind: "element",
      tag: element.tagName,
      children: [...element.childNodes].map(describeNode),
    };
  }
  function cloneSelection(body: Element, selectedRange: Range): ClonedSelection | null {
    const range = selectedRange.cloneRange();
    if (!body.contains(range.endContainer)) range.setEnd(body, body.childNodes.length);
    if (mentionAt(range.startContainer) || mentionAt(range.endContainer)) return null;
    const bodyTag = body.tagName;
    if (bodyTag === "P") return { bodyTag, text: range.toString() };
    return { bodyTag, nodes: [...range.cloneContents().childNodes].map(describeNode) };
  }
  function isSubmitter(button: Element | null | undefined, body: Element | null): boolean {
    return !!body && !!button?.matches(quoteSelectionButtons) && isSelectionSubmitter(body, button.previousElementSibling);
  }
  function update(): void {
    const selection = document.getSelection();
    const body = selected(selection);
    updateSelectionQuote(state, body, body ? cloneSelection(body, selection!.getRangeAt(0)) : null);
    for (const button of document.querySelectorAll<HTMLButtonElement>(quoteSelectionButtons)) {
      button.hidden = !isSubmitter(button, state.current);
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
    if (!isSubmitter(event.submitter, state.current)) {
      event.preventDefault();
      event.stopImmediatePropagation();
    }
  }, true);
  document.addEventListener("htmx:configRequest", (native: Event) => {
    const event = native as HtmxConfigRequestEvent;
    const target = event.target as Element;
    if (target.id !== QuoteFormID) return;
    clear();
    const button = event.detail.triggeringEvent?.submitter;
    // Reply carries the chip's current part; clearing it would select whole.
    if (!button?.matches('[name="reply"]')) delete event.detail.parameters[QuoteTextField];
    if (!button?.matches(quoteSelectionButtons)) return;
    // Always re-read: keyboard/programmatic submits need no pointer press,
    // and a changed or lost selection must never become a whole quote.
    update();
    if (!isSubmitter(button, state.current)) {
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
