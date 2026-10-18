(() => {
  const body = document.getElementById("message-body");
  const picker = document.getElementById("mention-picker");
  const search = document.getElementById("mention-search");
  const status = document.getElementById("mention-status");
  if (!body || !picker || !search) return;
  let composing = false, ended = -Infinity, query = null, dismissed = null, ready = false;
  const list = () => document.getElementById("mention-listbox");
  const options = () => [...list().querySelectorAll('[role="option"]')];
  // Mirror conversation.handleTokens' line-start/Unicode-space/'(' boundary.
  // Code detection is deliberately conservative; posting remains authoritative.
  const token = () => {
    if (composing || body.selectionStart !== body.selectionEnd) return null;
    const text = body.value.slice(0, body.selectionStart);
    const start = text.lastIndexOf("@");
    if (start < 0) return null;
    const before = text.slice(0, start), value = text.slice(start + 1);
    if ((before && !/[\p{White_Space}(]$/u.test(before)) || /\p{White_Space}/u.test(value)) return null;
    // An opening parenthesis can also be part of an autolink's path.
    if (/(?:^|[\p{White_Space}(])https?:\/\/[^\p{White_Space}<>"\u2066-\u2069\u200e\u200f\u061c]*$/u.test(before)) return null;
    const lines = before.split("\n");
    let fence = false;
    for (const line of lines.slice(0, -1)) {
      if (fence ? line === "```" : /^```[A-Za-z0-9+#.-]{0,20}$/.test(line)) fence = !fence;
    }
    if (fence) return null;
    let code = false;
    const line = lines.at(-1);
    for (let i = 0; i < line.length; i++) {
      if (line[i] === "\\") i++;
      else if (line[i] === "`") code = !code;
    }
    return code ? null : { start, value };
  };
  const close = () => {
    picker.hidden = true;
    body.removeAttribute("aria-activedescendant");
    options().forEach(option => option.setAttribute("aria-selected", "false"));
    status.textContent = "";
  };
  const activate = (index) => {
    const items = options();
    items.forEach((option, i) => option.setAttribute("aria-selected", String(i === index)));
    if (items[index]) {
      body.setAttribute("aria-activedescendant", items[index].id);
      items[index].scrollIntoView({ block: "nearest" });
    }
  };
  const show = () => {
    const current = token();
    if (!ready || !current || document.activeElement !== body || current.value === dismissed || list().dataset.query !== current.value) {
      close();
      return;
    }
    picker.hidden = false;
    status.textContent = list().dataset.count;
    activate(0);
  };
  const update = () => {
    const current = token();
    const next = current?.value ?? null;
    if (next === query) return;
    query = next;
    dismissed = null;
    close();
    if (next === null) return;
    search.elements.q.value = next;
    htmx.trigger(search, "mention-query");
  };
  const pick = (option) => {
    const current = token();
    if (!option || !current || picker.hidden || list().dataset.query !== current.value) {
      close();
      return;
    }
    body.setRangeText("@" + option.dataset.handle + " ", current.start, body.selectionStart, "end");
    close();
    body.dispatchEvent(new Event("input", { bubbles: true }));
  };
  body.addEventListener("compositionstart", () => { composing = true; query = null; close(); });
  body.addEventListener("compositionend", (event) => {
    composing = false;
    ended = event.timeStamp;
    update();
  });
  body.addEventListener("input", (event) => {
    if (event.isComposing) { composing = true; query = null; close(); }
    if (!composing) update();
  });
  document.addEventListener("selectionchange", () => { if (document.activeElement === body) update(); });
  body.addEventListener("blur", close);
  // The composer clears the textarea on send without an input event, so the
  // remembered query would make the same "@name" typed next a no-op.
  document.addEventListener("submit", (event) => {
    if (event.target.id !== "message-composer") return;
    query = null;
    dismissed = null;
    close();
  }, true);
  document.addEventListener("htmx:beforeSwap", (event) => {
    if (event.detail.target?.id === "mention-listbox") close();
  });
  document.addEventListener("htmx:afterSwap", (event) => {
    if (event.detail.target?.id !== "mention-listbox") return;
    ready = true;
    show();
  });
  // Capture runs before the composer's bubbling send handler, regardless of
  // script order. The IME commit Enter shares its 100 ms grace period.
  document.addEventListener("keydown", (event) => {
    if (event.target !== body || composing || event.isComposing || event.keyCode === 229 || (event.key === "Enter" && event.timeStamp - ended < 100) || picker.hidden) return;
    if (event.key === "Enter" && (event.ctrlKey || event.metaKey)) { close(); return; }
    if (!["ArrowDown", "ArrowUp", "Enter", "Tab", "Escape"].includes(event.key)) return;
    if (event.key !== "Escape" && !options().length) { close(); return; }
    event.preventDefault();
    event.stopImmediatePropagation();
    if (event.key === "Escape") { dismissed = token()?.value ?? null; close(); return; }
    if (event.key === "Enter" || event.key === "Tab") {
      if (!event.repeat) pick(list().querySelector('[aria-selected="true"]'));
      return;
    }
    const items = options(), active = items.findIndex(option => option.getAttribute("aria-selected") === "true");
    if (items.length) activate((active + (event.key === "ArrowDown" ? 1 : items.length - 1)) % items.length);
  }, true);
  picker.addEventListener("pointerdown", (event) => {
    if (event.target.closest('[role="option"]')) event.preventDefault();
  });
  picker.addEventListener("click", (event) => {
    if (!composing) pick(event.target.closest('[role="option"]'));
  });
})();
