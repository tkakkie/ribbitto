import test from "node:test";
import assert from "node:assert/strict";
import {
  appendMessageStatus,
  canCopyLink,
  createPendingCopies,
  maxMessageStatusEntries,
  type CopyClickInput,
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
  const cases: { name: string; event: CopyClickInput; writeText: unknown; want: boolean }[] = [
    { name: "unmodified primary click with writeText", event: baseClick, writeText: writeTextFn, want: true },
    { name: "non-primary click", event: { ...baseClick, button: 1 }, writeText: writeTextFn, want: false },
    { name: "metaKey held", event: { ...baseClick, metaKey: true }, writeText: writeTextFn, want: false },
    { name: "ctrlKey held", event: { ...baseClick, ctrlKey: true }, writeText: writeTextFn, want: false },
    { name: "shiftKey held", event: { ...baseClick, shiftKey: true }, writeText: writeTextFn, want: false },
    { name: "altKey held", event: { ...baseClick, altKey: true }, writeText: writeTextFn, want: false },
    { name: "missing writeText", event: baseClick, writeText: undefined, want: false },
  ];
  for (const row of cases) {
    assert.equal(canCopyLink(row.event, row.writeText), row.want, row.name);
  }
});

test("createPendingCopies settles only the latest pending copy per link and drops on close", () => {
  const cases = [
    ...[true, false].map((copied) => ({
      name: copied ? "latest copy succeeds; superseded results are stale" : "latest copy fails; superseded results are stale",
      run: (name: string) => {
        const pending = createPendingCopies();
        const link = {};
        const first = pending.start(link);
        const second = pending.start(link);
        assert.equal(pending.settle(link, first, true), "stale", name);
        assert.equal(pending.settle(link, first, false), "stale", name);
        assert.equal(pending.settle(link, second, copied), copied ? "copied" : "navigate", name);
      },
    })),
    { name: "menu close drops pending copy: late success shows nothing and late failure does not navigate", run: (name: string) => {
      const pending = createPendingCopies();
      const link = {};
      const copy = pending.start(link);
      pending.drop(link);
      assert.equal(pending.settle(link, copy, true), "stale", name);
      assert.equal(pending.settle(link, copy, false), "stale", name);
    } },
    { name: "menu close on one link does not affect another link in the WeakMap", run: (name: string) => {
      const pending = createPendingCopies();
      const firstLink = {};
      const secondLink = {};
      const firstCopy = pending.start(firstLink);
      const secondCopy = pending.start(secondLink);
      pending.drop(firstLink);
      assert.equal(pending.settle(firstLink, firstCopy, true), "stale", name);
      assert.equal(pending.settle(secondLink, secondCopy, true), "copied", name);
    } },
    { name: "token from another link never settles a different link", run: (name: string) => {
      const pending = createPendingCopies();
      const firstLink = {};
      const secondLink = {};
      const firstCopy = pending.start(firstLink);
      pending.start(secondLink);
      assert.equal(pending.settle(secondLink, firstCopy, true), "stale", name);
      assert.equal(pending.settle(secondLink, firstCopy, false), "stale", name);
    } },
  ];
  for (const row of cases) {
    row.run(row.name);
  }
});

interface StatusEntry {
  text: string;
  remove(): void;
}

// In-memory adapter for BoundedStatusLog; no DOM objects or browser APIs.
function createStatusLog(initial: string[] = []) {
  const makeNode = (text: string): StatusEntry => ({
    text,
    remove() {
      const idx = nodes.indexOf(this);
      if (idx !== -1) nodes.splice(idx, 1);
    },
  });
  const nodes = initial.map(makeNode);
  return {
    nodes,
    append: (node: StatusEntry) => nodes.push(node),
    get childNodes() { return nodes; },
    get firstChild() { return nodes[0] ?? null; },
    createNode: makeNode,
  };
}

test("appendMessageStatus appends newline-terminated label and drops entries beyond latest 10 from the front", () => {
  assert.equal(maxMessageStatusEntries, 10);
  const cases: { name: string; initial: string[]; labels: (string | undefined)[]; want: string[] }[] = [
    { name: "single entry", initial: [], labels: ["Link copied"], want: ["Link copied\n"] },
    { name: "missing label stringifies undefined", initial: [], labels: [undefined], want: ["undefined\n"] },
    {
      name: "exactly 10 entries kept", initial: [],
      labels: ["1", "2", "3", "4", "5", "6", "7", "8", "9", "10"],
      want: ["1\n", "2\n", "3\n", "4\n", "5\n", "6\n", "7\n", "8\n", "9\n", "10\n"],
    },
    {
      name: "11th entry drops 1st from front", initial: [],
      labels: ["1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"],
      want: ["2\n", "3\n", "4\n", "5\n", "6\n", "7\n", "8\n", "9\n", "10\n", "11\n"],
    },
    {
      name: "pre-populated status over 10 trims excess front entries",
      initial: ["a\n", "b\n", "c\n", "1\n", "2\n", "3\n", "4\n", "5\n", "6\n", "7\n", "8\n", "9\n"],
      labels: ["10"],
      want: ["1\n", "2\n", "3\n", "4\n", "5\n", "6\n", "7\n", "8\n", "9\n", "10\n"],
    },
  ];
  for (const row of cases) {
    const status = createStatusLog(row.initial);
    for (const label of row.labels) {
      appendMessageStatus(status, (text) => status.createNode(text), label);
    }
    assert.deepEqual(status.nodes.map((n) => n.text), row.want, row.name);
  }
});
