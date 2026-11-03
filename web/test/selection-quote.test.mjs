import test from "node:test";
import assert from "node:assert/strict";
import {
  bodyOf,
  bothEnds,
  excludesQuotes,
  isNonemptySelection,
  isSelectionSubmitter,
  quoteSelectionButtons,
  selectedBody,
  serializeSelection,
  updateSelectionQuote,
} from "../src/selection-quote.ts";

const ELEMENT_NODE = 1;
const TEXT_NODE = 3;
const COMMENT_NODE = 8;

const textNode = (text, parentElement = null) => ({ nodeType: TEXT_NODE, textContent: text, parentElement });
const commentNode = (text, parentElement = null) => ({ nodeType: COMMENT_NODE, textContent: text, parentElement });

function el(tagName, attrs = {}, children = [], extra = {}) {
  const node = {
    nodeType: ELEMENT_NODE,
    tagName: tagName.toUpperCase(),
    attrs: { ...attrs },
    childNodes: [],
    parentElement: null,
    ...extra,
    hasAttribute(name) { return Object.hasOwn(this.attrs, name); },
    getAttribute(name) { return Object.hasOwn(this.attrs, name) ? this.attrs[name] : null; },
    matches(selector) {
      if (selector === quoteSelectionButtons) return this.tagName === "BUTTON" && this.hasAttribute("data-quote-selection");
      if (selector === '[id^="body-message-"]') return typeof this.attrs.id === "string" && this.attrs.id.startsWith("body-message-");
      if (selector === "[data-quote-handle]") return this.hasAttribute("data-quote-handle");
      if (selector === "blockquote") return this.tagName === "BLOCKQUOTE";
      return false;
    },
    closest(selector) {
      for (let cur = this; cur; cur = cur.parentElement) if (cur.matches?.(selector)) return cur;
      return null;
    },
    contains(target) {
      for (let cur = target; cur; cur = cur.parentElement) if (cur === this) return true;
      return false;
    },
    querySelectorAll(selector) {
      const out = [];
      const walk = (n) => {
        for (const child of n.childNodes ?? []) {
          if (child.nodeType === ELEMENT_NODE) {
            if (child.matches(selector)) out.push(child);
            walk(child);
          }
        }
      };
      walk(this);
      return out;
    },
    get textContent() { return this.childNodes.map((c) => c.textContent ?? "").join(""); },
  };
  node.childNodes = children.map((child) => {
    const c = typeof child === "string" ? textNode(child, node) : child;
    c.parentElement = node;
    return c;
  });
  return node;
}

const makeFragment = (nodes) => ({
  childNodes: nodes,
  get children() { return nodes.filter((n) => n.nodeType === ELEMENT_NODE); },
  get textContent() { return nodes.map((n) => n.textContent ?? "").join(""); },
});

function makeRange(opts) {
  const {
    startContainer,
    endContainer = startContainer,
    fragmentNodes = [],
    tailText = "",
    toStringText = "selected text",
    clampedToStringText = toStringText,
    intersectedNodes = new Set(),
  } = opts;
  let endClamped = false, tailStartSet = false;
  return {
    startContainer,
    endContainer,
    setStart() { tailStartSet = true; },
    setEnd() { endClamped = true; },
    cloneRange() { return makeRange(opts); },
    cloneContents() { return makeFragment(tailStartSet ? [textNode(tailText)] : fragmentNodes); },
    intersectsNode(node) { return intersectedNodes.has(node); },
    toString() { return endClamped ? clampedToStringText : toStringText; },
  };
}

const makeSelection = ({ isCollapsed = false, rangeCount = 1, text = "selected text", range = null } = {}) => ({
  isCollapsed,
  rangeCount,
  getRangeAt(index) { assert.equal(index, 0); return range; },
  toString() { return text; },
});

test("isNonemptySelection rejects missing, collapsed or whitespace-only selections", () => {
  for (const [name, selection, want] of [
    ["null selection", null, false],
    ["undefined selection", undefined, false],
    ["collapsed selection with text", makeSelection({ isCollapsed: true, text: "hello" }), false],
    ["non-collapsed empty string", makeSelection({ isCollapsed: false, text: "" }), false],
    ["non-collapsed spaces and newlines only", makeSelection({ isCollapsed: false, text: "  \n\t\r " }), false],
    ["non-collapsed visible text", makeSelection({ isCollapsed: false, text: "  hello  " }), true],
  ]) {
    assert.equal(isNonemptySelection(selection), want, name);
  }
});

test("bodyOf resolves the enclosing body-message-* element from element or text endpoints", () => {
  const body = el("div", { id: "body-message-01950000000070008000000000000001" }, [el("p", {}, ["hello"])]);
  const paragraph = body.childNodes[0];
  const outside = el("div", { id: "message-01950000000070008000000000000001" }, ["outside"]);
  for (const [name, node, want] of [
    ["body element itself", body, body],
    ["nested paragraph element", paragraph, body],
    ["text node inside paragraph", paragraph.childNodes[0], body],
    ["element outside body-message-*", outside, null],
    ["text node outside body-message-*", outside.childNodes[0], null],
    ["detached text node with null parentElement", textNode("orphan", null), undefined],
    ["null node", null, undefined],
    ["undefined node", undefined, undefined],
  ]) {
    assert.equal(bodyOf(node), want, name);
  }
});

test("selectedBody enforces single-range, single-body, empty-tail and blockquote-exclusion rules", () => {
  const quoteBlock = el("blockquote", {}, [el("p", {}, ["quoted"])]);
  const firstBody = el("div", { id: "body-message-1111" }, [el("p", {}, ["first line"]), quoteBlock, el("p", {}, ["after quote"])]);
  const secondBody = el("div", { id: "body-message-2222" }, [el("p", {}, ["second message"])]);
  const outsideNode = el("span", {}, ["header"]);
  const firstText = firstBody.childNodes[0].childNodes[0];
  const afterQuoteText = firstBody.childNodes[2].childNodes[0];
  const secondText = secondBody.childNodes[0].childNodes[0];

  for (const [name, selection, want] of [
    ["null selection", null, null],
    ["collapsed selection", makeSelection({ isCollapsed: true, range: makeRange({ startContainer: firstText }) }), null],
    ["whitespace selection text", makeSelection({ text: "   ", range: makeRange({ startContainer: firstText }) }), null],
    ["rangeCount 0", makeSelection({ rangeCount: 0, range: makeRange({ startContainer: firstText }) }), null],
    ["rangeCount 2", makeSelection({ rangeCount: 2, range: makeRange({ startContainer: firstText }) }), null],
    ["selection inside one message body", makeSelection({ range: makeRange({ startContainer: firstText, endContainer: afterQuoteText }) }), firstBody],
    ["selection starting outside any message body", makeSelection({ range: makeRange({ startContainer: outsideNode.childNodes[0], endContainer: firstText }) }), null],
    ["selection crossing into a second message body (non-empty tail)", makeSelection({ range: makeRange({ startContainer: firstText, endContainer: secondText, tailText: "second" }) }), null],
    ["triple-click ending at offset zero of following element (empty tail)", makeSelection({ range: makeRange({ startContainer: firstText, endContainer: secondBody, tailText: "" }) }), firstBody],
    ["selection intersecting a blockquote inside the body", makeSelection({ range: makeRange({ startContainer: firstText, endContainer: afterQuoteText, intersectedNodes: new Set([quoteBlock]) }) }), null],
  ]) {
    assert.equal(selectedBody(selection), want, name);
  }
  assert.equal(bothEnds(null, makeSelection()), false);
  assert.equal(excludesQuotes(firstBody, makeSelection({ range: makeRange({ startContainer: firstText }) })), true);
});

test("serializeSelection handles plain P bodies, all allowed chat_v1 tags, block vs inline joining and rejected nodes", () => {
  const plainBody = el("p", { id: "body-message-aaaa" }, ["plain text body"]);
  const plainTextNode = plainBody.childNodes[0];
  const richBody = el("div", { id: "body-message-bbbb" }, [el("p", {}, ["hello"])]);
  const richTextNode = richBody.childNodes[0].childNodes[0];
  const followingSibling = el("button", {}, ["Quote selection"]);
  const resolvedMentionInBody = el("bdi", { "data-quote-handle": "alice" }, ["Alice (@alice)"]);
  const richBodyWithMention = el("div", { id: "body-message-cccc" }, [el("p", {}, ["hi ", resolvedMentionInBody, " there"])]);
  const mentionInnerText = resolvedMentionInBody.childNodes[0];
  const plainBodyWithMention = el("p", { id: "body-message-dddd" }, [resolvedMentionInBody]);

  for (const [name, body, range, want] of [
    ["single-P plain body returns range.toString() as is", plainBody, makeRange({ startContainer: plainTextNode, toStringText: "plain\n\ntext" }), "plain\n\ntext"],
    ["single-P body clamps end to body when endContainer is outside (triple-click tail)", plainBody, makeRange({ startContainer: plainTextNode, endContainer: followingSibling, toStringText: "plain text\nQuote selection", clampedToStringText: "plain text" }), "plain text"],
    [
      "inline partial selection inside rich body joins without newline",
      richBody,
      makeRange({
        startContainer: richTextNode,
        fragmentNodes: [
          textNode("lead "),
          el("strong", {}, ["bold ", el("em", {}, ["both"])]),
          textNode(" "),
          el("em", {}, ["italic"]),
          textNode(" "),
          el("code", {}, ["inline()"]),
          textNode(" "),
          el("a", { href: "https://example.com" }, ["https://example.com"]),
          el("br"),
          textNode("next line "),
          el("bdi", { "data-quote-handle": "bob" }, ["Bob (@bob)"]),
        ],
      }),
      "lead bold both italic inline() https://example.com\nnext line @bob",
    ],
    [
      "multiple P and PRE blocks in fragment join with single newline",
      richBody,
      makeRange({
        startContainer: richTextNode,
        fragmentNodes: [
          el("p", {}, ["first paragraph", el("br"), "second line"]),
          el("pre", {}, [el("code", {}, ["fmt.Println(1)\nfmt.Println(2)"])]),
          el("p", {}, ["after code ", el("bdi", { "data-quote-handle": "carol" }, ["Carol (@carol)"])]),
        ],
      }),
      "first paragraph\nsecond line\nfmt.Println(1)\nfmt.Println(2)\nafter code @carol",
    ],
    ["single P block in rich body fragment serializes children", richBody, makeRange({ startContainer: richTextNode, fragmentNodes: [el("p", {}, [el("strong", {}, ["only"]), textNode(" paragraph")])] }), "only paragraph"],
    ["single PRE block in rich body fragment serializes code", richBody, makeRange({ startContainer: richTextNode, fragmentNodes: [el("pre", {}, [el("code", {}, ["const x = 1;"])])] }), "const x = 1;"],
    ["cut mention at startContainer (text node inside [data-quote-handle]) returns null", richBodyWithMention, makeRange({ startContainer: mentionInnerText, endContainer: richBodyWithMention.childNodes[0].childNodes[2], fragmentNodes: [textNode("(@alice) there")] }), null],
    ["cut mention at endContainer (element [data-quote-handle] itself) returns null", richBodyWithMention, makeRange({ startContainer: richBodyWithMention.childNodes[0].childNodes[0], endContainer: resolvedMentionInBody, fragmentNodes: [textNode("hi ")] }), null],
    ["cut mention on P body also returns null before range.toString()", plainBodyWithMention, makeRange({ startContainer: mentionInnerText, toStringText: "@alice" }), null],
    ["unresolved mention (empty data-quote-handle='') returns null", richBody, makeRange({ startContainer: richTextNode, fragmentNodes: [el("p", {}, [textNode("hello "), el("bdi", { "data-quote-handle": "" }, ["@unknown user"])])] }), null],
    ["rejected BLOCKQUOTE tag in fragment returns null", richBody, makeRange({ startContainer: richTextNode, fragmentNodes: [el("blockquote", {}, [el("p", {}, ["quoted"])])] }), null],
    ["rejected SPAN tag nested inside P returns null", richBody, makeRange({ startContainer: richTextNode, fragmentNodes: [el("p", {}, [el("span", {}, ["unknown inline"])])] }), null],
    ["rejected DIV tag in fragment returns null", richBody, makeRange({ startContainer: richTextNode, fragmentNodes: [el("div", {}, ["nested div"])] }), null],
    ["non-element non-text node (comment node) in fragment returns null", richBody, makeRange({ startContainer: richTextNode, fragmentNodes: [el("p", {}, [textNode("ok"), commentNode("hidden")])] }), null],
    ["non-element non-text node at top level of fragment returns null", richBody, makeRange({ startContainer: richTextNode, fragmentNodes: [commentNode("top comment")] }), null],
  ]) {
    assert.equal(serializeSelection(body, range), want, name);
  }
});

test("updateSelectionQuote and isSelectionSubmitter clear whitespace/rejected text and match only the body's adjacent button", () => {
  const body1 = el("div", { id: "body-message-1111" }, [el("p", {}, ["hello world"])]);
  const body2 = el("div", { id: "body-message-2222" }, [el("p", {}, ["other message"])]);
  const text1 = body1.childNodes[0].childNodes[0];
  const button1 = el("button", { "data-quote-selection": "" }, [], { previousElementSibling: body1 });
  const button2 = el("button", { "data-quote-selection": "" }, [], { previousElementSibling: body2 });
  const nonQuoteButton = el("button", {}, [], { previousElementSibling: body1 });
  const detachedButton = el("button", { "data-quote-selection": "" }, [], { previousElementSibling: null });

  for (const [name, button, activeBody, want] of [
    ["matching button immediately after active body", button1, body1, true],
    ["button belonging to another message body", button2, body1, false],
    ["button without data-quote-selection attribute", nonQuoteButton, body1, false],
    ["button whose previousElementSibling is not the body", detachedButton, body1, false],
    ["null active body", button1, null, false],
    ["null button", null, body1, false],
    ["undefined button", undefined, body1, false],
  ]) {
    assert.equal(isSelectionSubmitter(button, activeBody), want, name);
  }

  for (const [name, selection, wantBody, wantText] of [
    ["valid selection keeps body and serialized text", makeSelection({ text: "hello", range: makeRange({ startContainer: text1, fragmentNodes: [textNode("hello")] }) }), body1, "hello"],
    ["serialized text that trims to empty (<br> only) clears current body to null", makeSelection({ text: "non-empty raw", range: makeRange({ startContainer: text1, fragmentNodes: [el("br")] }) }), null, "\n"],
    ["rejected markup returning null clears current body to null", makeSelection({ text: "unknown", range: makeRange({ startContainer: text1, fragmentNodes: [el("span", {}, ["unknown"])] }) }), null, null],
    ["no eligible body sets both current and text to null", makeSelection({ isCollapsed: true }), null, null],
  ]) {
    const state = { current: body2, text: "stale" };
    updateSelectionQuote(state, selection);
    assert.equal(state.current, wantBody, `${name} (current)`);
    assert.equal(state.text, wantText, `${name} (text)`);
  }
});
