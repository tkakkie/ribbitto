import test from "node:test";
import assert from "node:assert/strict";
import {
  activateMentionOptionIndex, canPickMentionCandidate, canShowMentionCandidates, classifyMentionKey,
  createMentionPickerState, cycleMentionOptionIndex, detectMentionToken, dismissMentionToken,
  endMentionComposition, formatMentionPick, mentionCompositionGraceMs, resetMentionPicker,
  startMentionComposition, stepMentionQuery,
} from "../src/mention-token.ts";

test("detectMentionToken matches @-token boundaries, quotes, autolinks, fences and code spans", () => {
  for (const [name, text, collapsed, composing, want] of [
    ["empty text", "", true, false, null],
    ["plain text without @", "plain text", true, false, null],
    ["bare @ at start of text", "@", true, false, { start: 0, value: "" }],
    ["@handle at start of text", "@tom", true, false, { start: 0, value: "tom" }],
    ["non-collapsed selection rejects token", "@tom", false, false, null],
    ["active IME composition rejects token", "@tom", true, true, null],
    ["closest @ before caret wins when earlier @ is also valid", "@first @second", true, false, { start: 7, value: "second" }],
    ["closest @ before caret wins after email-like token", "a@b.c @tom", true, false, { start: 6, value: "tom" }],
    ["closest @ with invalid boundary rejects even after earlier valid @", "@first x@second", true, false, null],
    ["ASCII space boundary", "x @tom", true, false, { start: 2, value: "tom" }],
    ["tab boundary", "x\t@tom", true, false, { start: 2, value: "tom" }],
    ["newline boundary", "x\n@tom", true, false, { start: 2, value: "tom" }],
    ["carriage-return boundary", "x\r@tom", true, false, { start: 2, value: "tom" }],
    ["form-feed boundary", "x\f@tom", true, false, { start: 2, value: "tom" }],
    ["vertical-tab boundary", "x\v@tom", true, false, { start: 2, value: "tom" }],
    ["NEL U+0085 whitespace boundary", "x\u0085@tom", true, false, { start: 2, value: "tom" }],
    ["no-break space U+00A0 boundary", "x\u00a0@tom", true, false, { start: 2, value: "tom" }],
    ["en space U+2002 boundary", "x\u2002@tom", true, false, { start: 2, value: "tom" }],
    ["ideographic space U+3000 boundary", "x\u3000@tom", true, false, { start: 2, value: "tom" }],
    ["line separator U+2028 boundary", "x\u2028@tom", true, false, { start: 2, value: "tom" }],
    ["opening parenthesis boundary at start", "(@tom", true, false, { start: 1, value: "tom" }],
    ["opening parenthesis boundary after text", "hello (@tom", true, false, { start: 7, value: "tom" }],
    ["non-ASCII text before space boundary", "x\n加藤 @tom", true, false, { start: 5, value: "tom" }],
    ["mandatory regression: >@ quoted line without space", ">@", true, false, { start: 1, value: "" }],
    ["mandatory regression: > @ quoted line with single space", "> @", true, false, { start: 2, value: "" }],
    ["mandatory regression: a>@ inline > before @ is not a quote boundary", "a>@", true, false, null],
    ["quoted line with handle and no space", ">@to", true, false, { start: 1, value: "to" }],
    ["quoted line with handle and single space", "> @to", true, false, { start: 2, value: "to" }],
    ["quoted line with two spaces keeps second space as boundary", ">  @to", true, false, { start: 3, value: "to" }],
    ["quoted line after newline", "a\n>@to", true, false, { start: 3, value: "to" }],
    ["nested >>@to is not a token", ">>@to", true, false, null],
    ["leading space before >@to is not a quote boundary", " >@to", true, false, null],
    ["word character before @", "x@tomoya", true, false, null],
    ["backslash before @", "\\@tomoya", true, false, null],
    ["email address", "a@tomoya.dev", true, false, null],
    ["closing parenthesis before @", ")@tom", true, false, null],
    ["value containing ASCII space", "@tom oya", true, false, null],
    ["value containing tab", "@tom\toya", true, false, null],
    ["value containing newline", "@tom\noya", true, false, null],
    ["value containing ideographic space U+3000", "@tom\u3000oya", true, false, null],
    ["https autolink path before @", "https://example.org/@tomoya", true, false, null],
    ["https autolink path with opening parenthesis before @", "https://example.org/(@tomoya", true, false, null],
    ["http autolink path with opening parenthesis before @", "http://example.org/(@tomoya", true, false, null],
    ["autolink after space with opening parenthesis before @", "see https://example.org/(@tomoya", true, false, null],
    ["autolink after opening parenthesis before @", "(https://example.org/(@tomoya", true, false, null],
    ["quoted https autolink path with opening parenthesis before @", ">https://example.org/(@to", true, false, null],
    ["quoted space https autolink path with opening parenthesis before @", "> https://example.org/(@to", true, false, null],
    ["space after autolink allows (@token", "https://example.org/ (@tom", true, false, { start: 22, value: "tom" }],
    ["angle bracket ending autolink allows (@token", "<https://example.org/>(@tom", true, false, { start: 23, value: "tom" }],
    ["double quote ending autolink allows (@token", '"https://example.org/"(@tom', true, false, { start: 23, value: "tom" }],
    ["bidi isolate U+2069 ending autolink allows (@token", "https://example.org/\u2069(@tom", true, false, { start: 22, value: "tom" }],
    ["bidi mark U+200E ending autolink allows (@token", "https://example.org/\u200e(@tom", true, false, { start: 22, value: "tom" }],
    ["arabic letter mark U+061C ending autolink allows (@token", "https://example.org/\u061c(@tom", true, false, { start: 22, value: "tom" }],
    ["non-http scheme with (@token is not an autolink exclusion", "ftp://example.org/(@tom", true, false, { start: 19, value: "tom" }],
    ["mandatory regression: line after unclosed fence", "```\n@tomoya", true, false, null],
    ["line after unclosed fence with language tag", "```go\n@tomoya", true, false, null],
    ["line after unclosed fence with 20-char info string", "```a2345678901234567890\n@tom", true, false, null],
    ["fence opening with 21-char info string does not open fence", "```a23456789012345678901\n@tom", true, false, { start: 25, value: "tom" }],
    ["fence opening with space in info string does not open fence", "```go lang\n@tom", true, false, { start: 11, value: "tom" }],
    ["mandatory regression: fence closed by exactly three backticks", "```go\ncode\n```\n@tom", true, false, { start: 15, value: "tom" }],
    ["fence not closed by closing line with info string", "```go\ncode\n```go\n@tom", true, false, null],
    ["fence not closed by four backticks", "```go\ncode\n````\n@tom", true, false, null],
    ["reopened fence after closed fence rejects token", "```\ncode\n```\n```ts\n@tom", true, false, null],
    ["mandatory regression: > quoted fence line before @ does not open a fence", "> ```\n@tom", true, false, { start: 6, value: "tom" }],
    ["quoted fence with language tag before >@to does not open a fence", ">```go\n>@to", true, false, { start: 8, value: "to" }],
    ["open fence is not closed by quoted >``` line", "```go\n>```\n>@to", true, false, null],
    ["unclosed backtick code span immediately before @", "`@tomoya", true, false, null],
    ["unclosed backtick code span with space before @", "` @tomoya", true, false, null],
    ["quoted unclosed backtick code span", ">` @to", true, false, null],
    ["quoted space unclosed backtick code span", "> `@to", true, false, null],
    ["closed backtick code span before @", "`code` @tom", true, false, { start: 7, value: "tom" }],
    ["quoted closed backtick code span before @", ">`code` @to", true, false, { start: 8, value: "to" }],
    ["backslash-escaped backtick does not open code span", "\\` @tom", true, false, { start: 3, value: "tom" }],
    ["escaped backslash before backtick still opens code span", "\\\\` @tom", true, false, null],
    ["unclosed code span containing escaped backtick", "`code\\` @tom", true, false, null],
  ]) {
    assert.deepEqual(detectMentionToken(text, collapsed, composing), want, name);
  }
});

test("stale-result, dismissal and query step rules guard opening, picking and duplicate searches", () => {
  for (const [name, ready, dismissed, token, focused, listQuery, want] of [
    ["not ready rejects show", false, null, { start: 0, value: "tom" }, true, "tom", false],
    ["null token rejects show", true, null, null, true, "tom", false],
    ["unfocused body rejects show", true, null, { start: 0, value: "tom" }, false, "tom", false],
    ["Escape-dismissed value rejects show", true, "tom", { start: 0, value: "tom" }, true, "tom", false],
    ["Escape-dismissed empty query rejects show", true, "", { start: 0, value: "" }, true, "", false],
    ["stale listbox data-query rejects show", true, null, { start: 0, value: "tom" }, true, "to", false],
    ["missing listbox data-query rejects show", true, null, { start: 0, value: "tom" }, true, null, false],
    ["matching ready focused non-dismissed query opens", true, null, { start: 0, value: "tom" }, true, "tom", true],
    ["different dismissed value allows new query to open", true, "to", { start: 0, value: "tom" }, true, "tom", true],
  ]) {
    const state = { ...createMentionPickerState(), ready, dismissed };
    assert.equal(canShowMentionCandidates(state, token, focused, listQuery), want, name);
  }
  const never = (label) => () => { throw new Error(`${label} must not be read`); };
  assert.equal(canShowMentionCandidates(createMentionPickerState(), { start: 0, value: "tom" }, never("bodyFocused"), never("listQuery")), false);
  assert.equal(canShowMentionCandidates({ ...createMentionPickerState(), ready: true, dismissed: "tom" }, { start: 0, value: "tom" }, true, never("listQuery")), false);

  for (const [name, hasOption, token, hidden, listQuery, want] of [
    ["missing option rejects pick", false, { start: 0, value: "tom" }, false, "tom", false],
    ["null token rejects pick", true, null, false, "tom", false],
    ["hidden picker rejects pick", true, { start: 0, value: "tom" }, true, "tom", false],
    ["stale listbox data-query rejects pick", true, { start: 0, value: "other" }, false, "tomoy", false],
    ["missing listbox data-query rejects pick", true, { start: 0, value: "tom" }, false, null, false],
    ["matching open listbox query allows pick", true, { start: 0, value: "tom" }, false, "tom", true],
  ]) {
    assert.equal(canPickMentionCandidate(hasOption, token, hidden, listQuery), want, name);
  }
  assert.equal(canPickMentionCandidate(true, { start: 0, value: "tom" }, true, never("listQuery")), false);

  const state = createMentionPickerState();
  assert.equal(stepMentionQuery(state, null), "unchanged");
  assert.equal(stepMentionQuery(state, { start: 0, value: "" }), "search");
  assert.equal(state.query, "");
  assert.equal(stepMentionQuery(state, { start: 0, value: "" }), "unchanged");
  dismissMentionToken(state, { start: 0, value: "" });
  assert.equal(state.dismissed, "");
  assert.equal(stepMentionQuery(state, { start: 0, value: "" }), "unchanged");
  assert.equal(state.dismissed, "");
  assert.equal(stepMentionQuery(state, { start: 0, value: "t" }), "search");
  assert.equal(state.query, "t");
  assert.equal(state.dismissed, null);
  dismissMentionToken(state, { start: 0, value: "t" });
  assert.equal(state.dismissed, "t");
  resetMentionPicker(state);
  assert.equal(state.query, null);
  assert.equal(state.dismissed, null);
  assert.equal(stepMentionQuery(state, { start: 0, value: "t" }), "search");
  assert.equal(stepMentionQuery(state, null), "closed");
  assert.equal(state.query, null);
  assert.equal(stepMentionQuery(state, { start: 0, value: "t" }), "search");
  assert.equal(state.query, "t");
  startMentionComposition(state);
  assert.equal(state.composing, true);
  assert.equal(state.query, null);
  let composingDuringTimestampRead = true;
  endMentionComposition(state, () => {
    composingDuringTimestampRead = state.composing;
    return 420;
  });
  assert.equal(composingDuringTimestampRead, false);
  assert.equal(state.composing, false);
  assert.equal(state.ended, 420);
  startMentionComposition(state);
  assert.throws(() => endMentionComposition(state, () => { throw new Error("timeStamp boom"); }), /timeStamp boom/);
  assert.equal(state.composing, false);
  assert.equal(state.ended, 420);
  startMentionComposition(state);
  endMentionComposition(state, 500);
  assert.equal(state.composing, false);
  assert.equal(state.ended, 500);
});

test("option index cycling and pick formatting", () => {
  const state = createMentionPickerState();
  assert.equal(state.activeIndex, -1);
  assert.equal(activateMentionOptionIndex(state, 0, 8), 0);
  assert.equal(state.activeIndex, 0);
  assert.equal(activateMentionOptionIndex(state, 0, 0), -1);
  assert.equal(state.activeIndex, -1);
  assert.equal(activateMentionOptionIndex(state, 3, 3), -1);

  for (const [name, active, count, key, want] of [
    ["no options ArrowDown is a no-op", -1, 0, "ArrowDown", null],
    ["no options ArrowUp is a no-op", 0, 0, "ArrowUp", null],
    ["ArrowDown from 0 advances to 1", 0, 3, "ArrowDown", 1],
    ["ArrowDown from middle advances to last", 1, 3, "ArrowDown", 2],
    ["ArrowDown from last wraps to 0", 2, 3, "ArrowDown", 0],
    ["ArrowUp from 0 wraps to last", 0, 3, "ArrowUp", 2],
    ["ArrowUp from last steps to middle", 2, 3, "ArrowUp", 1],
    ["ArrowUp from 1 steps to 0", 1, 3, "ArrowUp", 0],
    ["single option ArrowDown stays at 0", 0, 1, "ArrowDown", 0],
    ["single option ArrowUp stays at 0", 0, 1, "ArrowUp", 0],
    ["ArrowDown when none active (-1) selects 0", -1, 3, "ArrowDown", 0],
    ["ArrowUp when none active (-1) selects count - 2", -1, 3, "ArrowUp", 1],
  ]) {
    assert.equal(cycleMentionOptionIndex(active, count, key), want, name);
  }
  assert.equal(cycleMentionOptionIndex(0, 0, () => { throw new Error("key getter must not be read when count is 0"); }), null);

  assert.equal(formatMentionPick("tomoya"), "@tomoya ");
  assert.equal(formatMentionPick("person0"), "@person0 ");
  assert.equal(formatMentionPick(null), "@undefined ");
  assert.equal(formatMentionPick(undefined), "@undefined ");
});

test("classifyMentionKey handles consumed keys, repeat Enter/Tab, modifiers and IME guards", () => {
  assert.equal(mentionCompositionGraceMs, 100);
  const readyState = { composing: false, ended: -Infinity };
  const recentIME = { composing: false, ended: 1000 };

  for (const [name, event, state, hidden, wantAction, wantConsumed] of [
    ["plain ArrowDown cycles and consumes", { key: "ArrowDown" }, readyState, false, "cycle", true],
    ["plain ArrowUp cycles and consumes", { key: "ArrowUp" }, readyState, false, "cycle", true],
    ["plain Enter picks and consumes", { key: "Enter", repeat: false, timeStamp: 1200 }, readyState, false, "pick", true],
    ["plain Tab picks and consumes", { key: "Tab", repeat: false }, readyState, false, "pick", true],
    ["mandatory regression: repeat Enter is consumed without picking", { key: "Enter", repeat: true, timeStamp: 1200 }, readyState, false, "consume", true],
    ["mandatory regression: repeat Tab is consumed without picking", { key: "Tab", repeat: true }, readyState, false, "consume", true],
    ["plain Escape dismisses and consumes", { key: "Escape" }, readyState, false, "dismiss", true],
    ["Ctrl+Enter closes without consuming", { key: "Enter", ctrlKey: true, timeStamp: 1200 }, readyState, false, "close", false],
    ["Meta+Enter closes without consuming", { key: "Enter", metaKey: true, timeStamp: 1200 }, readyState, false, "close", false],
    ["Ctrl+Shift+Enter closes without consuming", { key: "Enter", ctrlKey: true, shiftKey: true, timeStamp: 1200 }, readyState, false, "close", false],
    ["Shift+Enter passes through", { key: "Enter", shiftKey: true, timeStamp: 1200 }, readyState, false, "ignore", false],
    ["Alt+Enter passes through", { key: "Enter", altKey: true, timeStamp: 1200 }, readyState, false, "ignore", false],
    ["Shift+Tab passes through", { key: "Tab", shiftKey: true }, readyState, false, "ignore", false],
    ["Ctrl+Tab passes through", { key: "Tab", ctrlKey: true }, readyState, false, "ignore", false],
    ["Alt+ArrowDown passes through", { key: "ArrowDown", altKey: true }, readyState, false, "ignore", false],
    ["Meta+ArrowUp passes through", { key: "ArrowUp", metaKey: true }, readyState, false, "ignore", false],
    ["Shift+Escape passes through", { key: "Escape", shiftKey: true }, readyState, false, "ignore", false],
    ["character key passes through", { key: "a" }, readyState, false, "ignore", false],
    ["Backspace passes through", { key: "Backspace" }, readyState, false, "ignore", false],
    ["ArrowLeft passes through", { key: "ArrowLeft" }, readyState, false, "ignore", false],
    ["hidden picker ignores Enter", { key: "Enter", timeStamp: 1200 }, readyState, true, "ignore", false],
    ["hidden picker ignores Tab", { key: "Tab" }, readyState, true, "ignore", false],
    ["hidden picker ignores Escape", { key: "Escape" }, readyState, true, "ignore", false],
    ["hidden picker ignores ArrowDown", { key: "ArrowDown" }, readyState, true, "ignore", false],
    ["hidden picker ignores Ctrl+Enter", { key: "Enter", ctrlKey: true, timeStamp: 1200 }, readyState, true, "ignore", false],
    ["composing state suppresses Enter", { key: "Enter", timeStamp: 1200 }, { composing: true, ended: -Infinity }, false, "ignore", false],
    ["composing state suppresses ArrowDown", { key: "ArrowDown" }, { composing: true, ended: -Infinity }, false, "ignore", false],
    ["event.isComposing suppresses Enter", { key: "Enter", isComposing: true, timeStamp: 1200 }, readyState, false, "ignore", false],
    ["event.isComposing suppresses Tab", { key: "Tab", isComposing: true }, readyState, false, "ignore", false],
    ["event.isComposing suppresses Escape", { key: "Escape", isComposing: true }, readyState, false, "ignore", false],
    ["keyCode 229 suppresses Enter", { key: "Enter", keyCode: 229, timeStamp: 1200 }, readyState, false, "ignore", false],
    ["keyCode 229 suppresses ArrowUp", { key: "ArrowUp", keyCode: 229 }, readyState, false, "ignore", false],
    ["keyCode 229 suppresses Tab", { key: "Tab", keyCode: 229 }, readyState, false, "ignore", false],
    ["Enter at 0 ms after compositionend is suppressed", { key: "Enter", timeStamp: 1000 }, recentIME, false, "ignore", false],
    ["Enter at 50 ms after compositionend is suppressed", { key: "Enter", timeStamp: 1050 }, recentIME, false, "ignore", false],
    ["Enter at 99.9 ms after compositionend is suppressed", { key: "Enter", timeStamp: 1099.9 }, recentIME, false, "ignore", false],
    ["Enter at 100 ms boundary after compositionend picks", { key: "Enter", timeStamp: 1100 }, recentIME, false, "pick", true],
    ["Tab within 100 ms of compositionend still picks", { key: "Tab", timeStamp: 1050 }, recentIME, false, "pick", true],
    ["ArrowDown within 100 ms of compositionend still cycles", { key: "ArrowDown", timeStamp: 1050 }, recentIME, false, "cycle", true],
    ["Escape within 100 ms of compositionend still dismisses", { key: "Escape", timeStamp: 1050 }, recentIME, false, "dismiss", true],
  ]) {
    let consumed = false;
    const got = classifyMentionKey(event, state, hidden, () => { consumed = true; });
    assert.equal(got, wantAction, name);
    assert.equal(consumed, wantConsumed, `${name} (consumed)`);
  }
});
