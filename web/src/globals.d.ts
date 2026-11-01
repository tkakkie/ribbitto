interface HtmxAjaxContext {
  source?: Element;
  target?: Element | string;
  swap?: string;
  event?: Event;
}

declare const htmx: {
  ajax(verb: string, path: string | null, context?: Element | string | HtmxAjaxContext): Promise<void>;
  process(target: Element): void;
  trigger(target: Element | string, name: string, detail?: unknown): boolean;
};

interface Window {
  htmx?: typeof htmx;
}
