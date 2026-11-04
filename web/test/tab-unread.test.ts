import test from "node:test";
import assert from "node:assert/strict";
import { tabUnread } from "../src/tab-unread-state.ts";

test("tab title prefix follows the channel total and caps at 99+", () => {
  for (const row of [
    { name: "no channels", channels: [], prefix: "" },
    { name: "zero", channels: ["0", "0"], prefix: "" },
    { name: "one message", channels: ["1"], prefix: "(1) " },
    { name: "multiple channels", channels: ["1", "2"], prefix: "(3) " },
    { name: "invalid count adds nothing", channels: ["bad", "1"], prefix: "(1) " },
    { name: "cap boundary", channels: ["99"], prefix: "(99) " },
    { name: "capped sum", channels: ["99", "1"], prefix: "(99+) " },
    { name: "beyond safe integer", channels: ["9007199254740993123456789"], prefix: "(99+) " },
  ]) {
    assert.equal(tabUnread(row.channels), row.prefix, row.name);
  }
});
