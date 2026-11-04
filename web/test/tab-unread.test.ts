import test from "node:test";
import assert from "node:assert/strict";
import { tabUnread } from "../src/tab-unread-state.ts";

test("tab title and favicon decisions follow channel totals, never topic subtotals", () => {
  const title = "general · Acme · ribbitto";
  for (const row of [
    { name: "no channels", channels: [], topics: [], prefix: "", badge: false },
    { name: "topic subtotal alone", channels: ["0"], topics: ["5"], prefix: "", badge: false },
    { name: "one topic message", channels: ["1"], topics: ["1"], prefix: "(1) ", badge: true },
    { name: "topic subtotal never raises total", channels: ["1"], topics: ["999"], prefix: "(1) ", badge: true },
    { name: "multiple channels", channels: ["1", "2"], topics: ["2"], prefix: "(3) ", badge: true },
    { name: "cap boundary", channels: ["99"], topics: [], prefix: "(99) ", badge: true },
    { name: "capped sum", channels: ["99", "1"], topics: [], prefix: "(99+) ", badge: true },
    { name: "beyond safe integer", channels: ["9007199254740993123456789"], topics: [], prefix: "(99+) ", badge: true },
    { name: "zero restored", channels: ["0", "0"], topics: [], prefix: "", badge: false },
  ]) {
    assert.deepEqual(tabUnread(title, row), { title: row.prefix + title, badge: row.badge }, row.name);
  }
});
