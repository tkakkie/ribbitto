export interface ReadState {
  succeeded: bigint | null;
  failures: number;
  intent: string | null;
}

export interface ReadOutcome extends ReadState {
  readonly flush: boolean;
}

/** Selects the cursor a read carries: Mark as read intent, else snapshot before any attempt, else latest applied. */
export function readCursor(snapshot: string, applied: string | null, attempted: bigint | null, intent: string | null): string {
  return intent ?? (attempted === null ? snapshot : applied || snapshot);
}

/** Reports whether a read for cursor is due above the last succeeded cursor. */
export function readDue(cursor: string, succeeded: bigint | null): boolean {
  return succeeded === null || BigInt(cursor) > succeeded;
}

/** Updates read state after a non-429 response and reports whether to flush pending work. */
export function settleRead(state: ReadState, attempted: bigint | null, successful: boolean, latest: string): ReadOutcome {
  if (successful) {
    state.succeeded = attempted;
    state.failures = 0;
    if (state.intent !== null && (attempted ?? 0n) >= BigInt(state.intent)) state.intent = null;
    return { ...state, flush: true };
  }
  // A failed Mark as read waits for another click; flush only strictly newer work.
  state.intent = null;
  return { ...state, flush: BigInt(latest) > (attempted ?? 0n) };
}

/** Computes the 429 retry delay in milliseconds with jittered backoff, Retry-After floor, and 32-bit signed cap. */
export function retryDelay(
  failures: number,
  retryAfter: string | null,
  random: number | (() => number) = () => Math.random(),
  now: number | (() => number) = () => Date.now(),
): number {
  const seconds = retryAfter === null ? NaN : Number(retryAfter);
  const after = Number.isFinite(seconds)
    ? seconds * 1000
    : Date.parse(retryAfter as string) - (typeof now === "function" ? now() : now);
  // The initial cap bounds our backoff; a server's longer minimum wins.
  const cap = Math.min(8000, 1000 * 2 ** Math.min(failures, 3));
  const sample = typeof random === "function" ? random() : random;
  // setTimeout treats a delay above 2^31 - 1 ms as zero, which would
  // retry at once instead of honouring a very long Retry-After.
  return Math.min(Math.max(cap * (0.5 + sample / 2), after || 0), 2147483647);
}
