/** Pending identity survives request-key rotation and delivery races. */
export interface PendingItem {
  readonly draftKey: string;
  readonly requestKey: string;
  readonly state: "queued" | "sending" | "failed" | "confirmed";
  readonly unconfirmed: boolean;
  readonly text: string;
  readonly postedMessage: string | null;
}
type MutablePendingItem = { -readonly [Key in keyof PendingItem]: PendingItem[Key] };
/** A restoration snapshot cannot change the queue's identity or uncertainty. */
export interface Restoration {
  readonly key: string;
  readonly text: string | null;
  readonly unconfirmed: boolean;
}
/** A response's routing is consumed once, rather than retained on the item. */
export interface PostedMessage { id: string; topic: string; seq: bigint }
/** Only messages actually applied to the current list count as delivered. */
export interface Delivery {
  ids: readonly string[];
  topic?: string;
  applied?: bigint;
  moved?: { fromTopic: string; ids: readonly string[] };
}
/** The item clause of the reload guard; ordinary requests and alerts are separate. */
export function holdsReload(items: readonly PendingItem[]): boolean {
  return items.some(item => item.state === "queued" || item.state === "sending" || item.state === "failed");
}

/** Creates page-local transitions; changed runs synchronously after each mutation. */
export function createComposerQueue(now: () => number, newKey: () => string, changed: () => void) {
  const items: MutablePendingItem[] = [];
  // A removed form's request still blocks dispatch until its completion.
  const requests = new Set<MutablePendingItem>();
  const refusedStatuses = [400, 404, 413, 422, 429];
  const removedKeyRetention = 5 * 60 * 1000;
  const holds = new Map<string, { key: string; at: number }>();
  let restored: { key: string; text: string | null; unconfirmed: boolean } | null = null;
  const holdID = (place: string, text: string) => JSON.stringify([place, text]);
  const expire = () => {
    for (const [id, hold] of holds) if (now() - hold.at >= removedKeyRetention) holds.delete(id);
  };
  const erase = (item: PendingItem) => { items.splice(items.indexOf(item), 1); };
  const delivered = (item: PendingItem, view: Delivery) => item.postedMessage !== null &&
    (view.ids.includes(item.postedMessage) || Boolean(view.topic && view.moved?.fromTopic === view.topic && view.moved.ids.includes(item.postedMessage)));
  const release = () => { restored = null; return newKey(); };
  const changeText = (text: string) => {
    if (restored && (text === "" || (restored.text !== null && restored.text !== text))) return release();
    return undefined;
  };
  return {
    get items(): readonly PendingItem[] { return items; },
    get restoration(): Restoration | null { return restored ? { ...restored } : null; },
    changeText,
    restore(key: string, text: string) { restored = { key, text, unconfirmed: true }; },
    sent(key: string) {
      for (const [id, hold] of holds) if (hold.key === key) holds.delete(id);
      return restored?.key === key ? release() : undefined;
    },
    ordinarySettled(status: number) {
      if (restored && (status === 200 || status === 409)) return release();
      if (restored && !refusedStatuses.includes(status)) restored.unconfirmed = true;
      return undefined;
    },
    pagehide() { holds.clear(); },
    expire,
    admit(text: string, place: string, ordinaryBusy = false, quoted = false): PendingItem | "busy" | "capacity" | "ordinary" {
      // The adapter must first call changeText and apply its fresh key to the
      // composer, including before busy/ordinary results (legacy submit order).
      changeText(text);
      if (items.length >= 10) return "capacity";
      if (ordinaryBusy) return "busy";
      const normalized = text.replace(/\r\n?/g, "\n").trim();
      if (quoted || !normalized || [...normalized].length > 4000) return items.length || requests.size ? "busy" : "ordinary";
      const id = holdID(place, text);
      expire();
      const hold = holds.get(id);
      holds.delete(id);
      const key = restored?.key || hold?.key || newKey();
      const item: MutablePendingItem = { draftKey: key, requestKey: key, state: "queued", text,
        unconfirmed: hold?.key === key || Boolean(restored?.key === key && restored.unconfirmed), postedMessage: null };
      restored = null;
      items.push(item);
      changed();
      return item;
    },
    dispatch(ordinaryBusy = false): PendingItem | undefined {
      if (ordinaryBusy || requests.size || items.some(item => item.state === "failed" && item.unconfirmed)) return undefined;
      const item = items.find(item => item.state === "queued");
      if (item) { item.state = "sending"; requests.add(item); changed(); }
      return item;
    },
    settle(pending: PendingItem, status: number, posted?: PostedMessage, view: Delivery = { ids: [] }) {
      const item = [...requests].find(item => item === pending);
      if (!item || item.state !== "sending") return undefined;
      requests.delete(item);
      let notice: "" | "repeat" | "notSent" | "stillUnconfirmed" | "unconfirmed" = "";
      if (status === 200) {
        item.state = "confirmed";
        item.unconfirmed = false;
        item.postedMessage = posted?.id ?? null;
        if (items.includes(item) && (delivered(item, view) || (posted && view.topic &&
            (posted.topic !== view.topic || (view.applied !== undefined && view.applied >= posted.seq && !view.ids.includes(posted.id)))))) erase(item);
      } else {
        item.state = "failed";
        if (status === 409) {
          item.unconfirmed = false;
          item.requestKey = newKey();
          notice = "repeat";
        } else if (refusedStatuses.includes(status)) {
          notice = item.unconfirmed ? "stillUnconfirmed" : "notSent";
        } else { item.unconfirmed = true; notice = "unconfirmed"; }
      }
      changed();
      return notice;
    },
    retry(pending: PendingItem): boolean {
      const item = items.find(item => item === pending);
      if (!item || item.state !== "failed") return false;
      if (!item.unconfirmed) { erase(item); items.push(item); }
      item.state = "queued";
      changed();
      return true;
    },
    edit(item: PendingItem, composerText: string, composing: boolean) {
      if (!items.includes(item) || item.state !== "failed" || composerText !== "" || composing) return undefined;
      // Edit identity survives non-empty edits; device drafts require exact text.
      restored = { key: item.requestKey, text: null, unconfirmed: item.unconfirmed };
      erase(item);
      changed();
      return { draftKey: item.draftKey, text: item.text, requestKey: item.requestKey, unconfirmed: item.unconfirmed };
    },
    remove(item: PendingItem, place: string): string | undefined {
      if (!items.includes(item)) return undefined;
      if (item.unconfirmed) { expire(); holds.set(holdID(place, item.text), { key: item.requestKey, at: now() }); }
      erase(item);
      changed();
      return item.draftKey;
    },
    deliver(view: Delivery): void {
      const removed = items.filter(item => delivered(item, view));
      if (!removed.length) return;
      for (const item of removed) erase(item);
      changed();
    },
  };
}
