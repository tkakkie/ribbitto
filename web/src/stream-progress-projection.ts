import { advancesCursor, shownCursor } from "./stream-cursor.ts";
import { createProgress, advanceProgress, type Progress, type ProgressInput } from "./stream-progress.ts";

/** Narrow projections keep compatibility writes testable without a DOM substitute. */
export type ProgressProjection = {
  applied(): string | undefined;
  shown(): string | undefined;
  url(): string | null;
  base: string;
  visible(): boolean;
  writeApplied(value: string): void;
  writeShown(value: string): void;
  writeURL(url: URL, after: string): void;
  notify(): void;
};

/** Compatible projection retains the legacy equality writes and notification timing. */
export function createStreamProgress(view: ProgressProjection) {
  let state: Progress | undefined;
  const initialize = () => {
    // Legacy attributes are parsed only when an outcome compares them, never at open.
    if (!state) {
      const snapshot = view.applied() || view.shown() || "0";
      state = { ...createProgress("0", "compatible", view.visible()),
        P0: snapshot, Ap: snapshot, Sh: snapshot, G: snapshot };
    }
    return state;
  };
  const report = (input: ProgressInput) => {
    const current = initialize();
    // Compatible reset closes the source, but already scheduled outcomes still settle.
    if (input.type === "terminal") return;
    if (input.type === "connection-created") {
      state = { ...current, Uc: input.after, Rc: null };
      return;
    }
    state = advanceProgress(current, input);
  };
  const show = () => {
    report({ type: "visibility-changed", visible: view.visible() });
    const applied = shownCursor(view.visible(), view.applied);
    if (!applied) return;
    view.writeShown(state!.Sh);
    view.notify();
  };
  const transport = (id: string) => {
    const url = new URL(view.url()!, view.base);
    if (!advancesCursor(id, url.searchParams.get("after"))) return;
    // Transport compares its own URL cursor, independently of the rendered snapshot.
    state = advanceProgress({ ...initialize(), G: url.searchParams.get("after")! }, { type: "checkpoint", id });
    view.writeURL(url, state.G);
  };
  return {
    report,
    snapshot: () => state,
    connectionCreated: (url: string) => report({ type: "connection-created", after: new URL(url, view.base).searchParams.get("after")! }),
    show,
    checkpoint: (id: string) => {
      transport(id);
    },
    applied: (id: string) => {
      // Keep the legacy fallback and parsing before any projection or notification.
      const advances = advancesCursor(id, view.applied() || view.shown());
      report({ type: "applied", id });
      if (!advances) return;
      view.writeApplied(state!.Ap);
      transport(id);
      show();
    },
  };
}
