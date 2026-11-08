export type PagePhase = "start" | "freeze" | "thaw" | "dispose";

export interface PageLifecycleCallbacks {
  phase(phase: PagePhase): void;
  pageshow(persisted: boolean): void;
}

/** Owns one module's page phases and listener lifetime without depending on the DOM. */
export function createPageLifecycle(callbacks: PageLifecycleCallbacks) {
  const controller = new AbortController();
  const dispose = () => {
    if (controller.signal.aborted) return;
    try {
      callbacks.phase("dispose");
    } finally {
      controller.abort();
    }
  };
  callbacks.phase("start");
  return {
    signal: controller.signal,
    pagehide(persisted: boolean) {
      if (controller.signal.aborted) return;
      if (persisted) callbacks.phase("freeze");
      else dispose();
    },
    pageshow(persisted: boolean) {
      if (controller.signal.aborted) return;
      if (persisted) callbacks.phase("thaw");
      if (!controller.signal.aborted) callbacks.pageshow(persisted);
    },
    dispose,
  };
}

/** Attaches an independent lifecycle for a module instance to its page. */
export function attachPageLifecycle(page: Window, callbacks: PageLifecycleCallbacks) {
  const lifecycle = createPageLifecycle(callbacks);
  const { signal } = lifecycle;
  page.addEventListener("pagehide", event => lifecycle.pagehide(event.persisted), { signal });
  page.addEventListener("pageshow", event => lifecycle.pageshow(event.persisted), { signal });
  return lifecycle;
}
