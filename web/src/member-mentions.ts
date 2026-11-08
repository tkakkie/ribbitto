import {
  MemberAttribute,
  MemberIDAttribute,
  OwnMentionAttribute,
  organizationStreamSelector,
  readAttribute,
} from "./contracts.gen.ts";
import { isOwnMention } from "./own-mention.ts";

export function startMemberMentions() {
  const selector = `bdi[${MemberIDAttribute}]`;
  const streamSelector = organizationStreamSelector();
  const highlight = (element: Element) => {
    const stream = element.closest(streamSelector);
    const member = stream ? readAttribute(stream, MemberAttribute) : undefined;
    element.toggleAttribute(OwnMentionAttribute, isOwnMention(member, readAttribute(element, MemberIDAttribute)));
  };
  const highlightWithin = (root: ParentNode) => {
    if ((root as Element).matches?.(selector)) highlight(root as Element);
    root.querySelectorAll(selector).forEach(highlight);
  };
  document.addEventListener("htmx:load", (event) => highlightWithin((event as CustomEvent<{ elt: ParentNode }>).detail.elt));
}
