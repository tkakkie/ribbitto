export interface UnreadEvent { channel: string; topic: string; author?: string; seq: number; eligible: boolean; applied: boolean }
export interface ResyncRequest { losses: number; received: boolean; success: boolean }
export interface UnreadState {
  snapshot: bigint; seen: bigint; lostThrough: bigint; recovering: boolean; losses: number;
  request: ResyncRequest | null; delay: number; events: Map<bigint, UnreadEvent>;
}

/** Keeps sequence arithmetic independent of the DOM and above Number's range. */
export function unreadState(snapshot: bigint): UnreadState {
  return { snapshot, seen: snapshot, lostThrough: 0n, recovering: false, losses: 0, request: null, delay: 250, events: new Map() };
}
export function observe(state: UnreadState, seq: bigint): UnreadState { return { ...state, seen: seq > state.seen ? seq : state.seen }; }
export function sequence(value: string | undefined): bigint {
  if (!/^\d+$/.test(value as string)) throw new Error("invalid snapshot sequence");
  return BigInt(value!);
}
export function unreadSequence(event: UnreadEvent | null): bigint {
  if (!event || !Number.isSafeInteger(event.seq) || event.seq < 0) throw new Error("invalid unread sequence");
  return BigInt(event.seq);
}
export function routing(event: UnreadEvent): boolean {
  const uuid = (value: unknown) => typeof value === "string" && /^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/.test(value);
  if (!uuid(event.channel)) throw new Error("invalid unread");
  if (event.topic === undefined) return false;
  if (!uuid(event.topic) || (event.author !== undefined && !uuid(event.author))) throw new Error("invalid routing");
  return true;
}
export function ignored(state: UnreadState, seq: bigint): boolean { return seq <= state.snapshot || state.events.has(seq); }
/** Overflow is reported before the triggering event is retained. */
export function overflow(state: UnreadState): { state: UnreadState; through: bigint } | null {
  if (state.events.size !== 1000) return null;
  let dropped = 0n;
  for (const seq of state.events.keys()) if (seq > dropped) dropped = seq;
  return { state: { ...state, events: new Map() }, through: dropped };
}
export function retain(state: UnreadState, seq: bigint, event: UnreadEvent, member: string | undefined): UnreadState {
  return { ...state, events: new Map(state.events).set(seq, { ...event, eligible: event.author !== undefined && event.author !== member, applied: false }) };
}
export function loss(state: UnreadState, through = state.seen): UnreadState {
  return { ...state, lostThrough: through > state.lostThrough ? through : state.lostThrough, recovering: true, losses: state.losses + 1 };
}
export function backoff(delay: number): number { return Math.min(delay * 2, 8000); }
export function retryWait(delay: number, random: number): number { return delay / 2 + random * delay / 2; }
/** Success and snapshot arrival can occur in either order; new losses reserve another request. */
export function complete(state: UnreadState): { state: UnreadState; covered: boolean | null } {
  if (!state.request?.success || !state.request.received) return { state, covered: null };
  const covered = state.request.losses === state.losses && state.snapshot >= state.lostThrough;
  return { state: { ...state, request: null, recovering: !covered, delay: covered ? 250 : backoff(state.delay) }, covered };
}
export function snapshot(state: UnreadState, seq: bigint): UnreadState {
  return { ...state, snapshot: seq, events: new Map([...state.events].filter(([held]) => held > seq).map(([held, event]) => [held, { ...event, applied: false }])) };
}
export function applicable(state: UnreadState, event: UnreadEvent): boolean { return !state.recovering && event.eligible && !event.applied; }
export function markApplied(state: UnreadState, seq: bigint): UnreadState {
  return { ...state, events: new Map(state.events).set(seq, { ...state.events.get(seq)!, applied: true }) };
}
export function resumeWait(random: number): number { return random * 2000; }
export function validCount(raw: string | undefined): boolean { return /^\d+$/.test(raw as string); }
export function incrementCount(raw: string): { count: string; text: string } {
  const count = BigInt(raw) + 1n;
  return { count: String(count), text: count >= 100n ? "99+" : String(count) };
}
export function incrementLabel(label: string, text: string): string { return label.replace(/\d+\+?/, text); }
