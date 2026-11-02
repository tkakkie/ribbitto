// Pure rules for actions-menu "Copy link" (#1159, #1830): click eligibility,
// per-link pending-copy tracking and the polite #message-status log bound.

export type CopyClickInput = {
  readonly button: number;
  readonly metaKey: boolean;
  readonly ctrlKey: boolean;
  readonly shiftKey: boolean;
  readonly altKey: boolean;
};

export type CopySettlement = "copied" | "navigate" | "stale";

export type PendingCopies<K extends object> = {
  start(link: K): object;
  settle(link: K, copy: object, copied: boolean): CopySettlement;
  drop(link: K): void;
};

export type BoundedStatusLog<T> = {
  append(node: T): void;
  readonly childNodes: { readonly length: number };
  readonly firstChild: { remove(): void } | null;
};

export const maxMessageStatusEntries = 10;

// Modified or non-primary clicks (new tab, new window, download, middle click)
// and browsers without the Clipboard API keep native link behaviour.
export function canCopyLink(event: CopyClickInput, writeText: unknown): boolean {
  if (!writeText || event.button !== 0) return false;
  return !(event.metaKey || event.ctrlKey || event.shiftKey || event.altKey);
}

// Closing or clicking again drops an earlier pending copy so a late resolution
// neither restores data-copied on a reopened menu nor announces or navigates
// for a menu the user already left.
export function createPendingCopies<K extends object>(): PendingCopies<K> {
  const pending = new WeakMap<K, object>();
  return {
    start(link: K): object {
      const copy = {};
      pending.set(link, copy);
      return copy;
    },
    settle(link: K, copy: object, copied: boolean): CopySettlement {
      if (pending.get(link) !== copy) return "stale";
      return copied ? "copied" : "navigate";
    },
    drop(link: K): void {
      pending.delete(link);
    },
  };
}

// Append rather than replace so screen readers still announce a second copy;
// bound the polite log so repeated copies cannot grow without limit.
export function appendMessageStatus<T>(
  status: BoundedStatusLog<T>,
  createNode: (text: string) => T,
  label: string | null | undefined,
): void {
  status.append(createNode(`${label}\n`));
  while (status.childNodes.length > maxMessageStatusEntries) {
    status.firstChild!.remove();
  }
}
