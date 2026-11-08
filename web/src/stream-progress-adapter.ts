import * as dom from "./contracts.gen.ts";
import { createStreamProgress } from "./stream-progress-projection.ts";

/** Only this adapter projects Progress onto the legacy reader contracts. */
export function streamProgressAdapter() {
  const connection = () => dom.getOrganizationStream()!;
  return createStreamProgress({
    applied: () => connection() ? dom.readAttribute(connection(), dom.AppliedCursorAttribute) ?? undefined : undefined,
    shown: () => connection() ? dom.readAttribute(connection(), dom.EventCursorAttribute) ?? undefined : undefined,
    url: () => connection()?.getAttribute("sse-connect") ?? null,
    base: location.href,
    visible: () => document.visibilityState === "visible",
    writeApplied: value => dom.writeAttribute(connection(), dom.AppliedCursorAttribute, value),
    writeShown: value => dom.writeAttribute(connection(), dom.EventCursorAttribute, value),
    writeURL: (url, after) => {
      url.searchParams.set("after", after);
      connection().setAttribute("sse-connect", url.pathname + url.search);
    },
    notify: () => document.dispatchEvent(new Event(dom.ReadAppliedEvent)),
  });
}
