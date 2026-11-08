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
  const report = (input: ProgressInput) => {
    state ??= createProgress(view.applied() || view.shown() ||
      new URL(view.url() || view.base, view.base).searchParams.get("after") || "0", "compatible", view.visible());
    state = advanceProgress(state, input);
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
    view.writeURL(url, state!.G);
  };
  return {
    report,
    snapshot: () => state,
    connectionCreated: (url: string) => report({ type: "connection-created", after: new URL(url, view.base).searchParams.get("after")! }),
    show,
    checkpoint: (id: string) => {
      report({ type: "checkpoint", id });
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
