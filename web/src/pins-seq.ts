/** Reports whether an incoming pins section is older than the one on screen. */
export function isStalePinsSeq(
  incomingSeq: string | null | undefined,
  currentSeq: string | null | undefined,
): boolean {
  if (!currentSeq || !incomingSeq) return false;
  return BigInt(incomingSeq) < BigInt(currentSeq);
}

/** Reports whether a message pin control's id is in the space-separated pinned list. */
export function isControlPinned(
  pinnedIDs: string | null | undefined,
  controlID: string | null | undefined,
): boolean {
  if (!pinnedIDs || !controlID) return false;
  return pinnedIDs.split(" ").includes(controlID);
}
