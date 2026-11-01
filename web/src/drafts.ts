// Pure decisions for local message drafts (decision 37 (d)).

export const DraftPrefix = "ribbitto.draft.v1:";
export const SignedOutNoticeKey = "ribbitto.signed-out.v1";
export const MaxTimerDelay = 2 ** 31 - 1;

const dayMs = 24 * 60 * 60 * 1000;
const uuidPattern = "[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}";
const uuidv7Pattern = "[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}";
const uuidRe = new RegExp("^" + uuidPattern + "$");
const uuidv7Re = new RegExp("^" + uuidv7Pattern + "$");
const draftKeyRe = new RegExp("^" + DraftPrefix.replaceAll(".", "\\.") + "(" + uuidPattern + "):(" + uuidv7Pattern + "):(" + uuidPattern + "):(feed|" + uuidPattern + ")$");

export interface DraftKeyParts { readonly member: string; readonly session: string; readonly channel: string; readonly topic: string }
export interface PageScope { readonly member: string; readonly session: string; readonly expires: number; readonly rendered: number; readonly deadline: number }
export interface DraftEntry { readonly member: string; readonly session: string; readonly expires: number; readonly text: string; readonly at: number; readonly key: string | null }
export interface ExpiryState { expired: boolean; readonly deadline: number; readonly expires: number }
export interface SendingSnapshot { readonly key: string; readonly inPlace: boolean }
export type CleanupAction = "ignore" | "keep" | "remove" | "validate";

export const isUUID = (value: unknown): value is string => typeof value === "string" && uuidRe.test(value);
export const isUUIDv7 = (value: unknown): value is string => typeof value === "string" && uuidv7Re.test(value);
export const uuidMillisecondPrefix = (id: string): string => id.slice(0, 8) + id.slice(9, 13);
export const draftKey = (member: string, session: string, channel: string | null | undefined, topic?: string | null): string =>
  DraftPrefix + member + ":" + session + ":" + (channel ?? "") + ":" + (topic || "feed");

export function parseDraftKey(key: string): DraftKeyParts | null {
  const match = draftKeyRe.exec(key);
  return match ? { member: match[1]!, session: match[2]!, channel: match[3]!, topic: match[4]! } : null;
}

export const isOwnDraftKey = (parts: DraftKeyParts | null, scope: Pick<PageScope, "member" | "session">): parts is DraftKeyParts =>
  parts !== null && parts.member === scope.member && parts.session === scope.session;
export const isSessionDraftKey = (key: string, session: string): boolean => parseDraftKey(key)?.session === session;

export function parsePageScope(raw: { member?: string | null; session?: string | null; expires?: string | null; rendered?: string | null }): PageScope | null {
  if (!isUUIDv7(raw.session) || raw.expires == null || raw.rendered == null) return null;
  const expires = Number(raw.expires), rendered = Number(raw.rendered);
  if (!Number.isSafeInteger(expires) || !Number.isSafeInteger(rendered)) return null;
  return { member: raw.member ?? "", session: raw.session, expires, rendered, deadline: expires - rendered };
}

export function parseDraftEntry(raw: string | null): unknown {
  if (typeof raw !== "string") return null;
  try { return JSON.parse(raw); } catch { return null; }
}

export function isValidDraftEntry(entry: unknown, scope: Pick<PageScope, "member" | "session" | "expires">, nowMs: number): entry is DraftEntry {
  if (!entry || typeof entry !== "object") return false;
  const c = entry as Record<string, unknown>;
  return c.member === scope.member && c.session === scope.session && c.expires === scope.expires &&
    typeof c.text === "string" && [...c.text].length <= 4000 &&
    typeof c.at === "number" && nowMs - c.at < 7 * dayMs && c.at - nowMs <= dayMs;
}

export const restoredSendKey = (entry: Pick<DraftEntry, "text" | "key">): string | null => entry.text && isUUID(entry.key) ? entry.key : null;

export function decideCleanupKey(key: string, horizon: string, member?: string): CleanupAction {
  if (!key.startsWith(DraftPrefix)) return "ignore";
  const parts = parseDraftKey(key);
  if (!parts) return "remove";
  if (member && parts.session === horizon) return parts.member === member ? "validate" : "remove";
  return uuidMillisecondPrefix(parts.session) < uuidMillisecondPrefix(horizon) ? "remove" : "keep";
}

export interface DraftStorage {
  getItem(key: string): string | null;
  removeItem(key: string): void;
}

function keepOwnEntry(
  raw: string | null,
  scope: Pick<PageScope, "member" | "session" | "expires"> | null | undefined,
  nowMs: number | (() => number),
): boolean {
  if (!scope) return false;
  try {
    const entry: unknown = JSON.parse(raw as string);
    return isValidDraftEntry(entry, scope, typeof nowMs === "function" ? nowMs() : nowMs);
  } catch {
    return false;
  }
}

export function sweepKeys(
  storage: DraftStorage,
  horizon: string,
  scope?: Pick<PageScope, "member" | "session" | "expires"> | null,
  nowMs: number | (() => number) = Date.now,
): void {
  for (const key of Object.keys(storage)) {
    const action = decideCleanupKey(key, horizon, scope?.member);
    if (action === "remove" || (action === "validate" && !keepOwnEntry(storage.getItem(key), scope, nowMs))) {
      storage.removeItem(key);
    }
  }
}

export function checkSessionExpired(state: ExpiryState, perfNow: number, dateNow: number): { readonly justExpired: boolean; readonly expired: boolean } {
  if (!state.expired && (perfNow >= state.deadline || dateNow >= state.expires)) {
    state.expired = true;
    return { justExpired: true, expired: true };
  }
  return { justExpired: false, expired: state.expired };
}

export const nextExpiryDelay = (deadline: number, perfNow: number): number => Math.min(deadline - perfNow, MaxTimerDelay);
export const normalizeDraftText = (raw: string): string => (!raw.trim() || [...raw].length > 4000 ? "" : raw);
export const nextEditTime = (prevAt: number, nowMs: number): number => Math.max(nowMs, prevAt + 1);
export const nextSendEditTime = (tabAt: number, nowMs: number, storedAt: unknown): number =>
  Math.max(nextEditTime(tabAt, nowMs), ((storedAt as number) || 0) + 1);
export const shouldWriteDraft = (storedAt: unknown, candidateAt: number): boolean => !((storedAt as number) >= candidateAt);

export function selectKeysToTrim(
  keys: readonly string[],
  scope: Pick<PageScope, "member" | "session">,
  limit: number,
  readAt: (key: string) => unknown,
  exceptKey?: string,
): string[] {
  const entries = keys
    .filter((k) => k !== exceptKey && isOwnDraftKey(parseDraftKey(k), scope))
    .map((k): [string, number] => [k, (readAt(k) as number) || 0]);
  entries.sort((a, b) => a[1] - b[1] || (a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0));
  return entries.slice(0, Math.max(0, entries.length - limit)).map(([k]) => k);
}

export function prepareSendSnapshot(
  detail: { readonly key?: string | null; readonly body?: unknown; readonly inPlace?: boolean } | null | undefined,
  tabAt: number,
  nowMs: number,
  storedAt: unknown,
): { readonly text: string; readonly at: number; readonly source: string; readonly savedKey: string | null; readonly sending: SendingSnapshot } | null {
  if (!detail?.key || typeof detail.body !== "string") return null;
  const text = normalizeDraftText(detail.body);
  return { text, at: nextSendEditTime(tabAt, nowMs, storedAt), source: detail.key, savedKey: text ? detail.key : null, sending: { key: detail.key, inPlace: Boolean(detail.inPlace) } };
}

export const matchSentSnapshot = (sending: SendingSnapshot | null | undefined, sentKey: string | null | undefined): SendingSnapshot | null =>
  sending && sending.key === sentKey ? sending : null;

export function canReplaceSnapshot(source: string | null | undefined, sendKey: string, inPlace: boolean, lastWrite: string | undefined, currentStored: string | null): boolean {
  const eligible = source === sendKey || (inPlace && source === null);
  return Boolean(eligible && lastWrite && currentStored === lastWrite);
}

export function pendingActionDecision(
  detail: { readonly kind?: string; readonly key?: string; readonly restoredKey?: string | null } | null | undefined,
): { readonly action: "save-edit"; readonly savedKey: string | null } | { readonly action: "replace"; readonly sendKey: string } | { readonly action: "none" } {
  if (detail?.kind === "edit") return { action: "save-edit", savedKey: detail.restoredKey || null };
  if (detail?.kind === "remove" && typeof detail.key === "string") return { action: "replace", sendKey: detail.key };
  return { action: "none" };
}

export function collectDraftPlaces(
  keys: readonly string[],
  scope: Pick<PageScope, "member" | "session" | "expires">,
  nowMs: number | (() => number) = Date.now,
  readRaw: (key: string) => string | null = () => null,
): Set<string> {
  const places = new Set<string>();
  for (const key of keys) {
    const parts = parseDraftKey(key);
    if (!isOwnDraftKey(parts, scope)) continue;
    const entry = parseDraftEntry(readRaw(key));
    if (isValidDraftEntry(entry, scope, typeof nowMs === "function" ? nowMs() : nowMs) && entry.text.trim()) {
      places.add(parts.channel);
      places.add(parts.channel + ":" + parts.topic);
    }
  }
  return places;
}

export const marked = (places: ReadonlySet<string>, channel: string, topic: string, open: boolean): boolean =>
  !open && places.has(topic === "feed" ? channel : channel + ":" + topic);

export function parseSidebarPlace(href: string, baseOrigin?: string): { readonly channel: string; readonly topic: string } {
  const parts = new URL(href, baseOrigin).pathname.split("/");
  return { channel: parts[4] ?? "", topic: parts[6] || "feed" };
}
