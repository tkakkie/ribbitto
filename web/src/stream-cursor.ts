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

/** Parking never replaces a source outside persisted cleanup or with a CLOSED source. */
export function shouldParkSource(hiding: boolean, readyState: number): boolean {
  return hiding && readyState !== 2;
}

/** Forget only after dispatch succeeds, so a throwing dispatch retains the parked source. */
export function restoreSource<T>(parked: T | null, persisted: boolean): T | null {
  return persisted ? parked : null;
}
