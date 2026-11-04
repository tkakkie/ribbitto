import test from "node:test";
import assert from "node:assert/strict";
import { tabUnread } from "../src/tab-unread-state.ts";

test("tab title prefix follows the channel total and caps at 99+", () => {
  for (const row of [
    { name: "no channels", channels: [], prefix: "" },
    { name: "zero", channels: ["0", "0"], prefix: "" },
    { name: "one message", channels: ["1"], prefix: "(1) " },
    { name: "multiple channels", channels: ["1", "2"], prefix: "(3) " },
    { name: "cap boundary", channels: ["99"], prefix: "(99) " },
    { name: "capped sum", channels: ["99", "1"], prefix: "(99+) " },
    { name: "beyond safe integer", channels: ["9007199254740993123456789"], prefix: "(99+) " },
  ]) {
    assert.equal(tabUnread(row.channels), row.prefix, row.name);
  }
});

test("tab adapter is a no-op without a favicon link or a sidebar channel list", async () => {
  const saved = Object.getOwnPropertyDescriptors(globalThis);
  try {
    for (const missing of ["icon", "sidebar", "both"]) {
      let touched = 0;
      const present = (kind: string) => (missing === kind || missing === "both" ? null : {});
      const effect = () => { touched++; };
      Object.assign(globalThis, {
        document: {
          title: "general",
          querySelector: (selector: string) => present(selector === 'link[rel="icon"]' ? "icon" : "sidebar"),
          createElement: effect,
        },
        Image: effect,
        MutationObserver: effect,
        matchMedia: effect,
      });
      await import(`../src/tab-unread.ts?${missing}`);
      assert.equal(touched, 0, missing);
      assert.equal((globalThis as { document: { title: string } }).document.title, "general", missing);
    }
  } finally {
    for (const key of ["document", "Image", "MutationObserver", "matchMedia"]) {
      if (saved[key]) Object.defineProperty(globalThis, key, saved[key]);
      else delete (globalThis as Record<string, unknown>)[key];
    }
  }
});
