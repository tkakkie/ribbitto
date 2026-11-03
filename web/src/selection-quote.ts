// Pure rules for selection quoting (#1829): ExtractChatV1 serialization over a
// structural description of the cloned selection and selection-quote eligibility.

const blockTags = ["P", "PRE"];
const inlineTags = ["P", "PRE", "STRONG", "EM", "CODE", "A"];

export type QuoteNode =
  | { readonly kind: "text"; readonly text: string }
  | {
      readonly kind: "element";
      readonly tag: string;
      readonly handle?: string | null;
      readonly children?: readonly QuoteNode[];
    }
  | { readonly kind: "other" };

export type ClonedSelection = {
  readonly bodyTag: string;
  readonly text?: string;
  readonly nodes?: readonly QuoteNode[];
};

export type SelectionQuoteState<B = unknown> = {
  current: B | null;
  text: string | null;
};

// Mirror ExtractChatV1, independently of browser paragraph separators and
// display names. Unknown markup or an unresolved mention is not quotable.
export function serializeSelection(selection: ClonedSelection): string | null {
  if (selection.bodyTag === "P") return selection.text ?? "";
  function inline(node: QuoteNode): string | null {
    if (node.kind === "text") return node.text;
    if (node.kind !== "element") return null;
    if (node.handle !== undefined && node.handle !== null) {
      return node.handle ? "@" + node.handle : null;
    }
    if (node.tag === "BR") return "\n";
    if (!inlineTags.includes(node.tag)) return null;
    return children(node.children ?? [], "");
  }
  function children(nodes: readonly QuoteNode[], separator: string): string | null {
    const parts = nodes.map(inline);
    return parts.includes(null) ? null : parts.join(separator);
  }
  const nodes = selection.nodes ?? [];
  const blocks = nodes.some((node) => node.kind === "element" && blockTags.includes(node.tag));
  return children(nodes, blocks ? "\n" : "");
}

export function isQuotableText(text: string | null | undefined): text is string {
  return !!text && text.trim() !== "";
}

export function isSelectionSubmitter(body: unknown, previousSibling: unknown): boolean {
  return !!body && previousSibling === body;
}

export function updateSelectionQuote<B>(
  state: SelectionQuoteState<B>,
  body: B | null | undefined,
  selection: ClonedSelection | null | undefined,
): void {
  state.current = body ?? null;
  state.text = state.current && selection ? serializeSelection(selection) : null;
  if (!isQuotableText(state.text)) state.current = null;
}
