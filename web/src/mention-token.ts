export type MentionToken = { start: number; value: string };
export type MentionPickerState = {
  composing: boolean; ended: number; query: string | null; dismissed: string | null; ready: boolean; activeIndex: number;
};
export type MentionKeyInput = {
  key: string; isComposing?: boolean; keyCode?: number; timeStamp?: number;
  ctrlKey?: boolean; metaKey?: boolean; shiftKey?: boolean; altKey?: boolean; repeat?: boolean;
};
export type MentionKeyAction = "ignore" | "close" | "dismiss" | "pick" | "consume" | "cycle";

export const mentionCompositionGraceMs = 100;

export const createMentionPickerState = (): MentionPickerState => ({
  composing: false, ended: -Infinity, query: null, dismissed: null, ready: false, activeIndex: -1,
});

// Mirror conversation.handleTokens' content-start/Unicode-space/'(' boundary.
// Code detection is deliberately conservative; posting remains authoritative.
export const detectMentionToken = (text: string, collapsed = true, composing = false): MentionToken | null => {
  if (composing || !collapsed) return null;
  const start = text.lastIndexOf("@");
  if (start < 0) return null;
  const lines = text.slice(0, start).split("\n"), value = text.slice(start + 1);
  const line = lines.at(-1)!.replace(/^> ?/, "");
  if ((line && !/[\p{White_Space}(]$/u.test(line)) || /\p{White_Space}/u.test(value)) return null;
  // An opening parenthesis can also be part of an autolink's path.
  if (/(?:^|[\p{White_Space}(])https?:\/\/[^\p{White_Space}<>"\u2066-\u2069\u200e\u200f\u061c]*$/u.test(line)) return null;
  let fence = false;
  for (const line of lines.slice(0, -1)) {
    if (fence ? line === "```" : /^```[A-Za-z0-9+#.-]{0,20}$/.test(line)) fence = !fence;
  }
  if (fence) return null;
  let code = false;
  for (let i = 0; i < line.length; i++) {
    if (line[i] === "\\") i++;
    else if (line[i] === "`") code = !code;
  }
  return code ? null : { start, value };
};

export const canShowMentionCandidates = (
  state: Pick<MentionPickerState, "ready" | "dismissed">,
  current: MentionToken | null,
  bodyFocused: boolean | (() => boolean),
  listQuery: (string | null | undefined) | (() => string | null | undefined),
): current is MentionToken =>
  state.ready && current !== null &&
  (typeof bodyFocused === "function" ? bodyFocused() : bodyFocused) &&
  current.value !== state.dismissed &&
  (typeof listQuery === "function" ? listQuery() : listQuery) === current.value;

export const canPickMentionCandidate = (
  hasOption: boolean,
  current: MentionToken | null,
  pickerHidden: boolean | string | (() => boolean | string),
  listQuery: (string | null | undefined) | (() => string | null | undefined),
): current is MentionToken =>
  hasOption && current !== null &&
  !(typeof pickerHidden === "function" ? pickerHidden() : pickerHidden) &&
  (typeof listQuery === "function" ? listQuery() : listQuery) === current.value;

export const stepMentionQuery = (
  state: Pick<MentionPickerState, "query" | "dismissed">,
  current: MentionToken | null,
): "unchanged" | "closed" | "search" => {
  const next = current?.value ?? null;
  if (next === state.query) return "unchanged";
  state.query = next;
  state.dismissed = null;
  return next === null ? "closed" : "search";
};

export const dismissMentionToken = (state: Pick<MentionPickerState, "dismissed">, current: MentionToken | null): void => {
  state.dismissed = current?.value ?? null;
};

export const resetMentionPicker = (state: Pick<MentionPickerState, "query" | "dismissed">): void => {
  state.query = null;
  state.dismissed = null;
};

export const startMentionComposition = (state: Pick<MentionPickerState, "composing" | "query">): void => {
  state.composing = true;
  state.query = null;
};

export const endMentionComposition = (state: Pick<MentionPickerState, "composing" | "ended">, timeStamp: number | (() => number)): void => {
  state.composing = false;
  state.ended = typeof timeStamp === "function" ? timeStamp() : timeStamp;
};

export const activateMentionOptionIndex = (state: Pick<MentionPickerState, "activeIndex">, index: number, count: number): number => {
  state.activeIndex = index >= 0 && index < count ? index : -1;
  return state.activeIndex;
};

export const cycleMentionOptionIndex = (activeIndex: number, count: number, key: string | (() => string)): number | null => {
  if (!count) return null;
  const pressed = typeof key === "function" ? key() : key;
  return (activeIndex + (pressed === "ArrowDown" ? 1 : count - 1)) % count;
};

export const formatMentionPick = (handle: string | null | undefined): string =>
  "@" + (handle ?? undefined) + " ";

export const classifyMentionKey = (
  event: MentionKeyInput,
  state: Pick<MentionPickerState, "composing" | "ended">,
  pickerHidden: boolean | string | (() => boolean | string),
  consume?: () => void,
): MentionKeyAction => {
  if (
    state.composing || event.isComposing || event.keyCode === 229 ||
    (event.key === "Enter" && (event.timeStamp as number) - state.ended < mentionCompositionGraceMs) ||
    (typeof pickerHidden === "function" ? pickerHidden() : pickerHidden)
  ) return "ignore";
  if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) return "close";
  if (event.shiftKey || event.altKey || event.ctrlKey || event.metaKey) return "ignore";
  if (!["ArrowDown", "ArrowUp", "Enter", "Tab", "Escape"].includes(event.key)) return "ignore";
  consume?.();
  if (event.key === "Escape") return "dismiss";
  if (event.key === "Enter" || event.key === "Tab") return event.repeat ? "consume" : "pick";
  return "cycle";
};
