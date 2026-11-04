import "./message-menu.ts";
import {
  CopiedAttribute,
  CopiedLabelAttribute,
  CopyLinkAttribute,
  getMessageStatus,
  writeAttribute,
} from "./contracts.gen.ts";
import {
  appendMessageStatus,
  canCopyLink,
  createPendingCopies,
} from "./copy-link.ts";

(() => {
  const linkSelector = `a[${CopyLinkAttribute}]`;
  const copiedLabelSelector = `[${CopiedLabelAttribute}]`;
  const pending = createPendingCopies<HTMLAnchorElement>();
  document.addEventListener("click", (event) => {
    const link = (event.target as { closest?: (s: string) => HTMLAnchorElement | null }).closest?.(linkSelector);
    if (!link || !canCopyLink(event, navigator.clipboard?.writeText)) return;
    event.preventDefault();
    const copy = pending.start(link);
    navigator.clipboard.writeText(link.href).then(
      () => {
        if (pending.settle(link, copy, true) !== "copied") return;
        writeAttribute(link, CopiedAttribute, "");
        const label = link.querySelector(copiedLabelSelector)?.textContent;
        const status = getMessageStatus();
        if (!status) return;
        appendMessageStatus(status, (text) => document.createTextNode(text), label);
      },
      () => {
        if (pending.settle(link, copy, false) === "navigate") location.assign(link.href);
      },
    );
  });
  document.addEventListener(
    "beforetoggle",
    (event) => {
      if ((event as ToggleEvent).newState !== "closed") return;
      for (const link of (event.target as { querySelectorAll?: (s: string) => NodeListOf<HTMLAnchorElement> }).querySelectorAll?.(linkSelector) ?? []) {
        pending.drop(link);
        link.removeAttribute(CopiedAttribute);
      }
    },
    true,
  );
})();
