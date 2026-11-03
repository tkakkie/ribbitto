// Pure rules for selection quoting (#1829): single-message body containment,
// blockquote exclusion, ExtractChatV1 serialization and submitter matching.
import { BodyMessageDOMPrefix, QuoteHandleAttribute, QuoteSelectionAttribute, readAttribute } from "./contracts.gen.ts";

const ELEMENT_NODE = 1;
const TEXT_NODE = 3;
const bodySelector = `[id^="${BodyMessageDOMPrefix}"]`;
const quoteHandleSelector = `[${QuoteHandleAttribute}]`;
const blockTags = ["P", "PRE"];
const inlineTags = ["P", "PRE", "STRONG", "EM", "CODE", "A"];

export const quoteSelectionButtons = `button[${QuoteSelectionAttribute}]`;

export type QuoteNode<E> = {
  readonly nodeType: number;
  readonly parentElement: { closest(selector: string): E | null } | null;
};

export type QuoteTreeNode = {
  readonly nodeType: number;
  readonly textContent: string | null;
  readonly tagName?: string;
  readonly childNodes?: Iterable<QuoteTreeNode>;
  hasAttribute?(name: string): boolean;
  getAttribute?(name: string): string | null;
};

export type QuoteFragment = {
  readonly textContent: string | null;
  readonly children: Iterable<{ readonly tagName: string }>;
  readonly childNodes: Iterable<QuoteTreeNode>;
};

export type QuoteRange<E, N = QuoteNode<E>, Q = E> = {
  readonly startContainer: N;
  readonly endContainer: N;
  cloneRange(): QuoteRange<E, N, Q>;
  setStart(node: E, offset: number): void;
  setEnd(node: E, offset: number): void;
  cloneContents(): QuoteFragment;
  intersectsNode(node: Q): boolean;
  toString(): string;
};

export type QuoteBodyElement<E = unknown, N = QuoteNode<E>, Q = E> = QuoteNode<E> & {
  readonly tagName: string;
  readonly childNodes: { readonly length: number };
  closest(selector: string): E | null;
  contains(other: N | null): boolean;
  querySelectorAll(selector: string): Iterable<Q>;
};

export type QuoteSelection<E, N = QuoteNode<E>, Q = E> = {
  readonly isCollapsed: boolean;
  readonly rangeCount: number;
  getRangeAt(index: number): QuoteRange<E, N, Q>;
  toString(): string;
};

export type QuoteButton = { matches(selector: string): boolean; readonly previousElementSibling?: unknown };
export type SelectionQuoteState<E> = { current: E | null; text: string | null };

export function bodyOf<E extends QuoteBodyElement<E, N, Q>, N extends QuoteNode<E> = QuoteNode<E>, Q = E>(
  node: N | null | undefined,
): E | null | undefined {
  return (node?.nodeType === ELEMENT_NODE ? (node as unknown as E) : node?.parentElement)?.closest(bodySelector);
}

export function bothEnds<E extends QuoteBodyElement<E, N, Q>, N extends QuoteNode<E> = QuoteNode<E>, Q = E>(
  body: E | null | undefined,
  selection: QuoteSelection<NoInfer<E>, N, Q>,
): body is E {
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

export function isNonemptySelection<S extends { readonly isCollapsed: boolean; toString(): string }>(
  selection: S | null | undefined,
): selection is S {
  return !!selection && !selection.isCollapsed && selection.toString().trim() !== "";
}

export function excludesQuotes<E extends QuoteBodyElement<E, N, Q>, N extends QuoteNode<E> = QuoteNode<E>, Q = E>(
  body: E,
  selection: QuoteSelection<NoInfer<E>, N, Q>,
): boolean {
  // Rich bodies may themselves contain blockquotes. A range crossing one
  // is excluded even when neither endpoint is inside the quote.
  return [...body.querySelectorAll("blockquote")].every((quote) => !selection.getRangeAt(0).intersectsNode(quote));
}

export function selectedBody<E extends QuoteBodyElement<E, N, Q>, N extends QuoteNode<E> = QuoteNode<E>, Q = E>(
  selection: QuoteSelection<E, N, Q> | null | undefined,
): E | null {
  if (!isNonemptySelection(selection) || selection.rangeCount !== 1) return null;
  const body = bodyOf<E, N, Q>(selection.getRangeAt(0).startContainer);
  return bothEnds(body, selection) && excludesQuotes(body, selection) ? body : null;
}

// Mirror ExtractChatV1, independently of browser paragraph separators and
// display names. Unknown markup or a cut/unresolved mention is not quotable.
export function serializeSelection<E extends QuoteBodyElement<E, N, Q>, N extends QuoteNode<E> = QuoteNode<E>, Q = E>(
  body: E,
  selectedRange: QuoteRange<NoInfer<E>, N, Q>,
): string | null {
  const range = selectedRange.cloneRange();
  if (!body.contains(range.endContainer)) range.setEnd(body, body.childNodes.length);
  const mentionAt = (node: N) =>
    (node.nodeType === ELEMENT_NODE ? (node as unknown as { closest(selector: string): unknown }) : node.parentElement)
      ?.closest(quoteHandleSelector);
  if (mentionAt(range.startContainer) || mentionAt(range.endContainer)) return null;
  if (body.tagName === "P") return range.toString();
  function inline(node: QuoteTreeNode): string | null {
    if (node.nodeType === TEXT_NODE) return node.textContent;
    if (node.nodeType !== ELEMENT_NODE) return null;
    if (node.hasAttribute!(QuoteHandleAttribute)) {
      const handle = readAttribute(node as unknown as Element, QuoteHandleAttribute);
      return handle ? "@" + handle : null;
    }
    if (node.tagName === "BR") return "\n";
    if (!inlineTags.includes(node.tagName!)) return null;
    return children(node, "");
  }
  function children(node: { readonly childNodes?: Iterable<QuoteTreeNode> }, separator: string): string | null {
    const parts = [...node.childNodes!].map(inline);
    return parts.includes(null) ? null : parts.join(separator);
  }
  const fragment = range.cloneContents();
  const blocks = [...fragment.children].some((node) => blockTags.includes(node.tagName));
  return children(fragment, blocks ? "\n" : "");
}

export function isSelectionSubmitter(button: QuoteButton | null | undefined, body: unknown): boolean {
  return !!body && !!button?.matches(quoteSelectionButtons) && button.previousElementSibling === body;
}

export function updateSelectionQuote<E extends QuoteBodyElement<E, N, Q>, N extends QuoteNode<E> = QuoteNode<E>, Q = E>(
  state: SelectionQuoteState<E>,
  selection: QuoteSelection<NoInfer<E>, N, Q> | null | undefined,
): void {
  state.current = selectedBody<E, N, Q>(selection);
  state.text = state.current ? serializeSelection(state.current, selection!.getRangeAt(0)) : null;
  if (!state.text?.trim()) state.current = null;
}
