import test from "node:test";
import assert from "node:assert/strict";
import {
  appendMessageStatus,
  canCopyLink,
  createPendingCopies,
  maxMessageStatusEntries,
} from "../src/copy-link.ts";

const baseClick = Object.freeze({
  button: 0,
  metaKey: false,
  ctrlKey: false,
  shiftKey: false,
  altKey: false,
});

const writeTextFn = () => Promise.resolve();

test("canCopyLink checks primary button, modifier keys and Clipboard API writeText", () => {
  for (const [name, event, writeText, want] of [
    ["unmodified primary click with writeText", baseClick, writeTextFn, true],
    ["middle click (button 1)", { ...baseClick, button: 1 }, writeTextFn, false],
    ["secondary click (button 2)", { ...baseClick, button: 2 }, writeTextFn, false],
    ["back button (button 3)", { ...baseClick, button: 3 }, writeTextFn, false],
    ["negative button (-1)", { ...baseClick, button: -1 }, writeTextFn, false],
    ["metaKey held", { ...baseClick, metaKey: true }, writeTextFn, false],
    ["ctrlKey held", { ...baseClick, ctrlKey: true }, writeTextFn, false],
    ["shiftKey held", { ...baseClick, shiftKey: true }, writeTextFn, false],
    ["altKey held", { ...baseClick, altKey: true }, writeTextFn, false],
    ["multiple modifiers held", { ...baseClick, metaKey: true, shiftKey: true }, writeTextFn, false],
    ["writeText undefined", baseClick, undefined, false],
    ["writeText null", baseClick, null, false],
  ]) {
    assert.equal(canCopyLink(event, writeText), want, name);
  }
});

test("createPendingCopies settles only the latest pending copy per link and drops on close", () => {
  for (const [, run] of [
    ["current copy succeeds -> copied", () => {
      const pending = createPendingCopies();
      const link = {};
      const copy = pending.start(link);
      assert.equal(pending.settle(link, copy, true), "copied");
    }],
    ["current copy fails -> navigate (opens the link)", () => {
      const pending = createPendingCopies();
      const link = {};
      const copy = pending.start(link);
      assert.equal(pending.settle(link, copy, false), "navigate");
    }],
    ["repeated click supersedes earlier pending copy: late success shows nothing, late failure does not navigate", () => {
      const pending = createPendingCopies();
      const link = {};
      const first = pending.start(link);
      const second = pending.start(link);
      assert.notEqual(first, second);
      assert.equal(pending.settle(link, first, true), "stale");
      assert.equal(pending.settle(link, first, false), "stale");
      assert.equal(pending.settle(link, second, true), "copied");
    }],
    ["repeated click where latest copy fails navigates only for latest", () => {
      const pending = createPendingCopies();
      const link = {};
      const first = pending.start(link);
      const second = pending.start(link);
      assert.equal(pending.settle(link, first, false), "stale");
      assert.equal(pending.settle(link, second, false), "navigate");
    }],
    ["menu close drops pending copy: late success shows nothing and late failure does not navigate", () => {
      const pending = createPendingCopies();
      const link = {};
      const copy = pending.start(link);
      pending.drop(link);
      assert.equal(pending.settle(link, copy, true), "stale");
      assert.equal(pending.settle(link, copy, false), "stale");
    }],
    ["menu close on one link does not affect another link in the WeakMap", () => {
      const pending = createPendingCopies();
      const firstLink = {};
      const secondLink = {};
      const firstCopy = pending.start(firstLink);
      const secondCopy = pending.start(secondLink);
      pending.drop(firstLink);
      assert.equal(pending.settle(firstLink, firstCopy, true), "stale");
      assert.equal(pending.settle(secondLink, secondCopy, true), "copied");
    }],
    ["token from another link never settles a different link", () => {
      const pending = createPendingCopies();
      const firstLink = {};
      const secondLink = {};
      const firstCopy = pending.start(firstLink);
      pending.start(secondLink);
      assert.equal(pending.settle(secondLink, firstCopy, true), "stale");
      assert.equal(pending.settle(secondLink, firstCopy, false), "stale");
    }],
  ]) {
    run();
  }
});

function createFakeStatusLog(initial = []) {
  const makeNode = (text) => ({
    text,
    remove() {
      const idx = nodes.indexOf(this);
      if (idx !== -1) nodes.splice(idx, 1);
    },
  });
  const nodes = initial.map(makeNode);
  return {
    nodes,
    append: (node) => nodes.push(node),
    get childNodes() { return nodes; },
    get firstChild() { return nodes[0] ?? null; },
    createNode: makeNode,
  };
}

test("appendMessageStatus appends newline-terminated label and drops entries beyond latest 10 from the front", () => {
  assert.equal(maxMessageStatusEntries, 10);
  for (const [name, initial, labels, want] of [
    ["single entry", [], ["Link copied"], ["Link copied\n"]],
    ["missing label stringifies undefined", [], [undefined], ["undefined\n"]],
    [
      "exactly 10 entries kept",
      [],
      ["1", "2", "3", "4", "5", "6", "7", "8", "9", "10"],
      ["1\n", "2\n", "3\n", "4\n", "5\n", "6\n", "7\n", "8\n", "9\n", "10\n"],
    ],
    [
      "11th entry drops 1st from front",
      [],
      ["1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"],
      ["2\n", "3\n", "4\n", "5\n", "6\n", "7\n", "8\n", "9\n", "10\n", "11\n"],
    ],
    [
      "15 entries keep only latest 10 in order",
      [],
      ["1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12", "13", "14", "15"],
      ["6\n", "7\n", "8\n", "9\n", "10\n", "11\n", "12\n", "13\n", "14\n", "15\n"],
    ],
    [
      "pre-populated status over 10 trims excess front entries",
      ["a\n", "b\n", "c\n", "1\n", "2\n", "3\n", "4\n", "5\n", "6\n", "7\n", "8\n", "9\n"],
      ["10"],
      ["1\n", "2\n", "3\n", "4\n", "5\n", "6\n", "7\n", "8\n", "9\n", "10\n"],
    ],
  ]) {
    const status = createFakeStatusLog(initial);
    for (const label of labels) {
      appendMessageStatus(status, (text) => status.createNode(text), label);
    }
    assert.deepEqual(status.nodes.map((n) => n.text), want, name);
  }
});
