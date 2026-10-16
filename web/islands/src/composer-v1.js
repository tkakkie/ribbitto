// The composer island's mount script (#1152 prototype). The server renders
// the whole form, which posts without JavaScript; the island replaces only
// its textarea with one it renders, keeping what was typed before it mounted.
import { mount, unmount } from "svelte";
import Composer from "./Composer.svelte";

// A message's URL (decision 36) lands on the message its fragment names,
// which the browser has brought into view; only other loads start at the
// newest entry. Not composer state: moved unchanged from
// message-composer-v8.js, so base and head do the same work on load.
const newest = () => {
  const list = document.getElementById("message-list");
  const target = document.getElementById(location.hash.slice(1));
  if (target && target.parentElement?.id === "message-items") return;
  if (list) list.scrollTop = list.scrollHeight;
};
newest();

const islands = new WeakMap();

document.addEventListener("htmx:load", (event) => {
  const root = event.detail.elt;
  const form = root.id === "message-composer" ? root : root.querySelector("#message-composer");
  if (!form || islands.has(form)) return;
  const server = form.elements.body;
  const focused = document.activeElement === server;
  const { selectionStart, selectionEnd, selectionDirection } = server;
  const island = mount(Composer, {
    target: form,
    anchor: server,
    props: {
      id: server.id,
      name: server.name,
      rows: server.rows,
      dir: server.dir,
      className: server.className,
      describedBy: server.getAttribute("aria-describedby"),
      invalid: server.getAttribute("aria-invalid") === "true",
      value: server.value,
    },
  });
  islands.set(form, island);
  server.remove();
  const body = form.elements.body;
  body.setSelectionRange(selectionStart, selectionEnd, selectionDirection);
  if (focused) body.focus({ preventScroll: true });
});

// htmx cleans up every element it removes; Svelte 5 components are functions,
// so the island is destroyed only through unmount().
document.addEventListener("htmx:beforeCleanupElement", (event) => {
  const island = islands.get(event.target);
  if (island === undefined) return;
  islands.delete(event.target);
  unmount(island);
});
