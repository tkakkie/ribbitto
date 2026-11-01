export interface ReadState {
  readonly succeeded: bigint | null;
  readonly failures: number;
  readonly intent: string | null;
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
export function settleRead(state: ReadState, attempted: bigint, successful: boolean, latest: string): ReadOutcome {
  if (successful) {
    const intent = state.intent !== null && attempted < BigInt(state.intent) ? state.intent : null;
    return { succeeded: attempted, failures: 0, intent, flush: true };
  }
  // A failed Mark as read waits for another click; flush only strictly newer work.
  return { succeeded: state.succeeded, failures: state.failures, intent: null, flush: BigInt(latest) > attempted };
}

/** Computes the 429 retry delay in milliseconds with jittered backoff, Retry-After floor, and 32-bit signed cap. */
export function retryDelay(failures: number, retryAfter: string | null, random: number, now: number): number {
  const seconds = retryAfter === null || retryAfter.trim() === "" ? NaN : Number(retryAfter);
  const after = Number.isFinite(seconds) ? seconds * 1000 : retryAfter === null ? NaN : Date.parse(retryAfter) - now;
  // The initial cap bounds our backoff; a server's longer minimum wins.
  const cap = Math.min(8000, 1000 * 2 ** Math.min(failures, 3));
  // setTimeout treats a delay above 2^31 - 1 ms as zero, which would
  // retry at once instead of honouring a very long Retry-After.
  return Math.min(Math.max(cap * (0.5 + random / 2), after || 0), 2147483647);
}
