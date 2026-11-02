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
export function createComposerQueue(now: () => number, newKey: () => string, changed: (settled?: PendingItem, notice?: string) => void) {
  const items: MutablePendingItem[] = [];
  // A removed form's request still blocks dispatch until its completion.
  const requests = new Set<MutablePendingItem>();
  const refusedStatuses = [400, 404, 413, 422, 429];
  const removedKeyRetention = 5 * 60 * 1000;
  const holds = new Map<string, { key: string; at: number; owner: PendingItem }>();
  let restored: { key: string; text: string | null; unconfirmed: boolean } | null = null;
  const holdID = (place: string, text: string) => JSON.stringify([place, text]);
  // A timer must expire its own hold even when the clock has not advanced.
  const expire = (removed?: PendingItem) => {
    for (const [id, hold] of holds) if (removed !== undefined ? hold.owner === removed : now() - hold.at >= removedKeyRetention) holds.delete(id);
  };
  const erase = (item: PendingItem) => { items.splice(items.indexOf(item), 1); };
  const delivered = (item: PendingItem, view: Delivery) => item.postedMessage !== null &&
    (view.ids.includes(item.postedMessage) || Boolean(view.topic && view.moved?.fromTopic === view.topic && view.moved.ids.includes(item.postedMessage)));
  const release = (renew = newKey) => { restored = null; return renew(); };
  const changeText = (text: string) => {
    if (restored && (text === "" || (restored.text !== null && restored.text !== text))) return release();
    return undefined;
  };
  const next = (ordinaryBusy = false) => ordinaryBusy || requests.size || items.some(item => item.state === "failed" && item.unconfirmed)
    ? undefined : items.find(item => item.state === "queued");
  const response = (item: MutablePendingItem, posted: PostedMessage, view: Delivery) => {
    item.postedMessage = posted.id;
    const routed = Boolean(view.topic && (posted.topic !== view.topic ||
      (view.applied !== undefined && view.applied >= posted.seq && !view.ids.includes(posted.id))));
    if (items.includes(item) && (delivered(item, view) || routed)) erase(item);
    return routed;
  };
  return {
    get items(): readonly PendingItem[] { return items; },
    get removedKeyRetention(): number { return removedKeyRetention; },
    get restoration(): Restoration | null { return restored ? { ...restored } : null; },
    changeText,
    restore(key: string, text: string) { restored = { key, text, unconfirmed: true }; },
    sent(key: string | null, renew = newKey) {
      for (const [id, hold] of holds) if (hold.key === key) holds.delete(id);
      return (restored ? restored.key : null) === key ? release(renew) : undefined;
    },
    ordinarySettled(status: number) {
      if (restored && (status === 200 || status === 409)) return release();
      if (restored && !refusedStatuses.includes(status)) restored.unconfirmed = true;
      return undefined;
    },
    forgetRestoration() { restored = null; },
    pagehide() { holds.clear(); },
    expire,
    admit(text: string, place: string, ordinaryBusy = false, quoted = false, prepare?: () => void): PendingItem | "busy" | "capacity" | "ordinary" {
      // The adapter first calls changeText and applies any returned key to the composer;
      // ordinary/busy results use that composer key, not a queued item's key.
      // An item takes the restored key, else an unexpired Remove hold for the same place/raw text, else newKey().
      // As in the legacy composer, enqueue rotates the composer key, discarding any released key.
      changeText(text);
      if (items.length >= 10) return "capacity";
      if (ordinaryBusy) return "busy";
      const normalized = text.replace(/\r\n?/g, "\n").trim();
      if (quoted || !normalized || [...normalized].length > 4000) return items.length || requests.size ? "busy" : "ordinary";
      // Prepare the admitted form before key generation, which may throw.
      prepare?.();
      const id = holdID(place, text);
      const hold = holds.get(id);
      holds.delete(id);
      const key = restored?.key || (hold && now() - hold.at < removedKeyRetention ? hold.key : newKey());
      const item: MutablePendingItem = { draftKey: key, requestKey: key, state: "queued", text,
        unconfirmed: hold?.key === key || Boolean(restored?.key === key && restored.unconfirmed), postedMessage: null };
      restored = null;
      items.push(item);
      changed();
      return item;
    },
    next,
    dispatch(ordinaryBusy = false): PendingItem | undefined {
      const item = next(ordinaryBusy);
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
        if (posted) response(item, posted, view);
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
      changed(item, notice);
      return notice;
    },
    // htmx swaps the response before completing the request; project its ID then.
    response(pending: PendingItem, id: string) {
      const item = [...requests].find(item => item === pending);
      if (!item) return;
      item.postedMessage = id;
      changed();
    },
    route(pending: PendingItem, posted: PostedMessage, view: Delivery) {
      const item = items.find(item => item === pending) || [...requests].find(item => item === pending);
      if (!item) return false;
      const connected = items.includes(item);
      const routed = response(item, posted, view);
      if (connected && !items.includes(item)) changed();
      return routed;
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
      if (item.unconfirmed) { expire(); holds.set(holdID(place, item.text), { key: item.requestKey, at: now(), owner: item }); }
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
