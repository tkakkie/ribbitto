/** BigInt preserves sequence precision and the legacy exceptions for malformed cursors. */
export function advancesCursor(incoming: string, current: string | null | undefined): boolean {
  return BigInt(incoming) >= BigInt(current as string);
}

/** Hidden applications resume transport without publishing a shown cursor. */
export function shownCursor(visible: boolean, applied: () => string | undefined): string | undefined {
  // The hidden path must not read the cursor, including a throwing accessor.
  if (!visible) return undefined;
  return applied() || undefined;
}

/** The boundary belongs to Load older; zero admits every nonnegative sequence. */
export function admitsMove(sequence: string | undefined, oldest: bigint): boolean {
  return BigInt(sequence as string) >= oldest;
}

/** Find the first higher sequence, leaving equal sequences in delivery order. */
export function higherSequence(child: string | undefined, incoming: string | undefined): boolean {
  return BigInt(child as string) > BigInt(incoming as string);
}

/** Removal of older live-region nodes is silent under aria-relevant=additions. */
export function announcementOverflow(count: number): boolean { return count > 10; }

/** The queue supplies its predicate; alerts and the ordinary request remain separate inputs. */
export function resetHeld(request: boolean | undefined, failedAlert: boolean, pending: boolean): boolean {
  return Boolean(request || failedAlert || pending);
}

/** Uniform reset jitter, sampled only when a new reload timer is needed. */
export function resetDelay(random: number): number { return random * 5000; }

/** Plain lifecycle state; the adapter retains the actual source for requested actions. */
export type SourceState = { hiding: boolean; parked: boolean };
type SourceTransition = { state: SourceState; action: "none" | "cleanup" | "park" | "error" };

/** Only persisted pagehide opens the synchronous cleanup window. */
export function pageHide(state: SourceState, persisted: boolean): SourceTransition {
  return persisted ? { state: { ...state, hiding: true }, action: "cleanup" } : { state, action: "none" };
}

/** Run in the adapter's finally block, including when cleanup throws. */
export function finishPageHide(state: SourceState): SourceState {
  return { ...state, hiding: false };
}

/** The adapter must test state.hiding before reading detail.source.readyState,
 * then reread detail.source only when the park action is requested, as in v15. */
export function parkSource(state: SourceState, readyState: number): SourceTransition {
  return state.hiding && readyState !== 2
    ? { state: { ...state, parked: true }, action: "park" } : { state, action: "none" };
}

/** Dispatch first; a throwing dispatch must retain the parked state. */
export function restoreSource(state: SourceState, persisted: boolean): SourceTransition {
  return { state, action: persisted && state.parked ? "error" : "none" };
}

/** A successful error dispatch completes restoration exactly once. */
export function forgetSource(state: SourceState): SourceState {
  return { ...state, parked: false };
}
