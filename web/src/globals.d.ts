declare const htmx: {
  process(target: Element): void;
  trigger(target: Element | string, name: string, detail?: unknown): boolean;
};
