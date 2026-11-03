import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import {
  isQuotableText,
  isSelectionSubmitter,
  serializeSelection,
  updateSelectionQuote,
} from "../src/selection-quote.ts";

const txt = (text) => ({ kind: "text", text });
const el = (tag, children = [], handle = undefined) => ({
  kind: "element",
  tag,
  ...(handle !== undefined ? { handle } : {}),
  children,
});
const mention = (handle, tag = "BDI") => ({ kind: "element", tag, handle });
const other = () => ({ kind: "other" });

test("isQuotableText rejects missing, empty or whitespace-only strings", () => {
  for (const [name, value, want] of [
    ["null", null, false],
    ["undefined", undefined, false],
    ["empty string", "", false],
    ["spaces, tabs and newlines only", "  \n\t\r ", false],
    ["single newline from <br>", "\n", false],
    ["visible text", "hello", true],
    ["visible text with surrounding whitespace", "  hello  ", true],
  ]) {
    assert.equal(isQuotableText(value), want, name);
  }
});

test("serializeSelection handles single-P bodies, all allowed chat_v1 tags, block vs inline joining, mentions and rejected nodes", () => {
  for (const [name, selection, want] of [
    [
      "single-P plain body returns range text as is (preserving blank lines)",
      { bodyTag: "P", text: "plain\n\ntext" },
      "plain\n\ntext",
    ],
    [
      "single-P body with clamped triple-click text returns clamped range text",
      { bodyTag: "P", text: "plain text" },
      "plain text",
    ],
    [
      "single-P body ignores fragment nodes when present and returns range text",
      { bodyTag: "P", text: "raw range text", nodes: [el("SPAN", [txt("ignored")])] },
      "raw range text",
    ],
    [
      "single-P body with omitted text defaults to empty string",
      { bodyTag: "P" },
      "",
    ],
    [
      "inline partial selection inside rich body joins without newline",
      {
        bodyTag: "DIV",
        nodes: [
          txt("lead "),
          el("STRONG", [txt("bold "), el("EM", [txt("both")])]),
          txt(" "),
          el("EM", [txt("italic")]),
          txt(" "),
          el("CODE", [txt("inline()")]),
          txt(" "),
          el("A", [txt("https://example.com")]),
          el("BR"),
          txt("next line "),
          mention("bob"),
        ],
      },
      "lead bold both italic inline() https://example.com\nnext line @bob",
    ],
    [
      "multiple P and PRE blocks in fragment join with single newline",
      {
        bodyTag: "DIV",
        nodes: [
          el("P", [txt("first paragraph"), el("BR"), txt("second line")]),
          el("PRE", [el("CODE", [txt("fmt.Println(1)\nfmt.Println(2)")])]),
          el("P", [txt("after code "), mention("carol")]),
        ],
      },
      "first paragraph\nsecond line\nfmt.Println(1)\nfmt.Println(2)\nafter code @carol",
    ],
    [
      "single P block in rich body fragment serializes children without extra newline",
      {
        bodyTag: "DIV",
        nodes: [el("P", [el("STRONG", [txt("only")]), txt(" paragraph")])],
      },
      "only paragraph",
    ],
    [
      "single PRE block in rich body fragment serializes code",
      {
        bodyTag: "DIV",
        nodes: [el("PRE", [el("CODE", [txt("const x = 1;")])])],
      },
      "const x = 1;",
    ],
    [
      "each allowed inline tag (STRONG, EM, CODE, A, BR) and resolved mention nested in P",
      {
        bodyTag: "DIV",
        nodes: [
          el("P", [
            el("STRONG", [txt("b")]),
            el("EM", [txt("i")]),
            el("CODE", [txt("c")]),
            el("A", [txt("https://example.test")]),
            el("BR"),
            mention("alice"),
          ]),
        ],
      },
      "bichttps://example.test\n@alice",
    ],
    [
      "element with non-empty handle serializes as @handle before tag check",
      {
        bodyTag: "DIV",
        nodes: [mention("alice", "SPAN")],
      },
      "@alice",
    ],
    [
      "element with omitted children defaults to empty content",
      {
        bodyTag: "DIV",
        nodes: [{ kind: "element", tag: "STRONG" }, txt("ok")],
      },
      "ok",
    ],
    [
      "rich body with omitted nodes defaults to empty string",
      { bodyTag: "DIV" },
      "",
    ],
    [
      "unresolved mention (empty data-quote-handle='') at top level returns null",
      {
        bodyTag: "DIV",
        nodes: [mention("")],
      },
      null,
    ],
    [
      "unresolved mention (empty data-quote-handle='') nested inside P returns null",
      {
        bodyTag: "DIV",
        nodes: [el("P", [txt("hello "), mention("")])],
      },
      null,
    ],
    [
      "BDI element without handle (null or undefined) is rejected as unknown tag",
      {
        bodyTag: "DIV",
        nodes: [el("BDI", [txt("Alice")], null)],
      },
      null,
    ],
    [
      "rejected BLOCKQUOTE tag in fragment returns null",
      {
        bodyTag: "DIV",
        nodes: [el("BLOCKQUOTE", [el("P", [txt("quoted")])])],
      },
      null,
    ],
    [
      "rejected SPAN tag nested inside P returns null",
      {
        bodyTag: "DIV",
        nodes: [el("P", [el("SPAN", [txt("unknown inline")])])],
      },
      null,
    ],
    [
      "rejected DIV tag in fragment returns null",
      {
        bodyTag: "DIV",
        nodes: [el("DIV", [txt("nested div")])],
      },
      null,
    ],
    [
      "lowercase tag name ('p') is rejected",
      {
        bodyTag: "DIV",
        nodes: [el("p", [txt("lowercase")])],
      },
      null,
    ],
    [
      "non-element non-text node nested inside P returns null",
      {
        bodyTag: "DIV",
        nodes: [el("P", [txt("ok"), other()])],
      },
      null,
    ],
    [
      "non-element non-text node at top level of fragment returns null",
      {
        bodyTag: "DIV",
        nodes: [other()],
      },
      null,
    ],
  ]) {
    assert.equal(serializeSelection(selection), want, name);
  }
});

test("updateSelectionQuote and isSelectionSubmitter enforce non-empty trimmed text and adjacent body matching", () => {
  const body1 = { id: "body-message-1111" };
  const body2 = { id: "body-message-2222" };

  for (const [name, activeBody, previousSibling, want] of [
    ["button immediately after active body (previousElementSibling === body)", body1, body1, true],
    ["button belonging to another message body", body1, body2, false],
    ["button whose previousElementSibling is null", body1, null, false],
    ["button whose previousElementSibling is undefined", body1, undefined, false],
    ["null active body with non-null sibling", null, body1, false],
    ["null active body with null sibling", null, null, false],
    ["undefined active body with undefined sibling", undefined, undefined, false],
    ["empty-string body never matches empty-string sibling", "", "", false],
  ]) {
    assert.equal(isSelectionSubmitter(activeBody, previousSibling), want, name);
  }

  for (const [name, body, selection, wantBody, wantText] of [
    [
      "valid rich selection keeps body and serialized text",
      body1,
      { bodyTag: "DIV", nodes: [txt("hello")] },
      body1,
      "hello",
    ],
    [
      "valid single-P selection keeps body and range text",
      body1,
      { bodyTag: "P", text: "plain paragraph" },
      body1,
      "plain paragraph",
    ],
    [
      "serialized text that trims to empty (<br> only) clears current body to null",
      body1,
      { bodyTag: "DIV", nodes: [el("BR")] },
      null,
      "\n",
    ],
    [
      "whitespace-only single-P text clears current body to null",
      body1,
      { bodyTag: "P", text: "   \n\t " },
      null,
      "   \n\t ",
    ],
    [
      "rejected markup returning null clears current body to null",
      body1,
      { bodyTag: "DIV", nodes: [el("SPAN", [txt("unknown")])] },
      null,
      null,
    ],
    [
      "cut mention or invalid range in adapter (null ClonedSelection) clears current and text to null",
      body1,
      null,
      null,
      null,
    ],
    [
      "no eligible body sets both current and text to null even if selection is provided",
      null,
      { bodyTag: "DIV", nodes: [txt("hello")] },
      null,
      null,
    ],
    [
      "undefined body sets both current and text to null",
      undefined,
      undefined,
      null,
      null,
    ],
  ]) {
    const state = { current: body2, text: "stale" };
    updateSelectionQuote(state, body, selection);
    assert.equal(state.current, wantBody, `${name} (current)`);
    assert.equal(state.text, wantText, `${name} (text)`);
  }
});

test("selection-quote.ts stays DOM-free with no DOM types, methods or contract accessors", () => {
  const source = readFileSync(new URL("../src/selection-quote.ts", import.meta.url), "utf8");
  assert.doesNotMatch(source, /\bimport\b/, "selection-quote.ts must not import DOM or contract helpers");
  for (const forbidden of [
    "document",
    "window",
    "Element",
    "HTMLElement",
    "Node",
    "Range",
    "Selection",
    "DocumentFragment",
    "ParentNode",
    "Event",
    "CustomEvent",
    "nodeType",
    "tagName",
    "textContent",
    "childNodes",
    "parentElement",
    "previousElementSibling",
    "closest",
    "contains",
    "matches",
    "querySelector",
    "querySelectorAll",
    "getAttribute",
    "hasAttribute",
    "readAttribute",
    "writeAttribute",
    "cloneRange",
    "cloneContents",
    "intersectsNode",
    "setStart",
    "setEnd",
    "getRangeAt",
    "rangeCount",
    "isCollapsed",
  ]) {
    assert.doesNotMatch(
      source,
      new RegExp(`\\b${forbidden}\\b`),
      `selection-quote.ts must not mention DOM identifier ${forbidden}`,
    );
  }
});
