/** These reasons preserve the first broken delivery for recovery diagnostics. */
export type FailureReason = "apply-exception" | "apply-verification" | "decode-missing-item"
  | "decode-missing-id" | "decode-invalid-id" | "decode-non-monotonic" | "history-reapply";

/** Strings retain exact server sequences beyond the safe integer range. */
export type Failure = Readonly<{ reason: FailureReason; id: string | null }>;

/** Page progress survives source recreation; connection observations do not. */
export type Progress = Readonly<{
  mode: "compatible" | "strict";
  P0: string;
  Ap: string;
  Sh: string;
  G: string;
  F: Failure | null;
  Uc: string;
  Rc: string | null;
  visible: boolean;
  terminal: boolean;
}>;

/** Dispatch is separate from outcomes because receiving an id does not prove application.
 * The adapter reports outcomes only for decoded durable frames; other kinds only dispatch. */
export type ProgressInput =
  | { type: "connection-created"; after: string }
  | { type: "frame-dispatched"; kind: string; lastEventId: string }
  | { type: "applied" | "contract-no-op" | "checkpoint"; id: string }
  | { type: "failed"; reason: FailureReason; id: string | null }
  | { type: "terminal" }
  | { type: "visibility-changed"; visible: boolean };

function decimal(id: string): boolean { return id.length > 0 && !/[^0-9]/.test(id); }
function highest(current: string, incoming: string): string {
  return decimal(incoming) && BigInt(incoming) >= BigInt(current) ? incoming : current;
}

/** Invalid snapshot input is a caller error, unlike untrusted raw dispatch ids. */
export function createProgress(snapshot: string, mode: Progress["mode"], visible: boolean): Progress {
  if (!decimal(snapshot)) throw new Error("invalid progress snapshot");
  return { mode, P0: snapshot, Ap: snapshot, Sh: snapshot, G: snapshot,
    F: null, Uc: snapshot, Rc: null, visible, terminal: false };
}

/** No side effects lets both policies share the same delivery history before wiring. */
export function advanceProgress(state: Progress, input: ProgressInput): Progress {
  switch (input.type) {
    case "connection-created":
      // A newly created source must not skip progress the page has not reached.
      if (!decimal(input.after) || BigInt(input.after) > BigInt(state.G)) {
        throw new Error("invalid progress connection after");
      }
      return { ...state, Uc: input.after, Rc: null };
    case "frame-dispatched": {
      // Ephemeral frames inherit lastEventId, so only the kind grants durability.
      const durable = ["message", "messages-moved", "checkpoint", "reset"].includes(input.kind);
      return durable && decimal(input.lastEventId) ? { ...state, Rc: input.lastEventId } : state;
    }
    case "visibility-changed":
      return { ...state, visible: input.visible, Sh: input.visible ? state.Ap : state.Sh };
    default:
      if (state.terminal) return state;
  }
  switch (input.type) {
    case "terminal": return { ...state, terminal: true };
    case "failed":
      return state.F ? state : { ...state, F: { reason: input.reason, id: input.id !== null && decimal(input.id) ? input.id : null } };
    case "checkpoint":
      return state.mode === "strict" && state.F ? state : { ...state, G: highest(state.G, input.id) };
    case "applied":
    case "contract-no-op": {
      if (state.mode === "strict" && state.F) return state;
      const Ap = highest(state.Ap, input.id);
      return { ...state, Ap, Sh: state.visible ? Ap : state.Sh, G: highest(state.G, Ap) };
    }
  }
}
