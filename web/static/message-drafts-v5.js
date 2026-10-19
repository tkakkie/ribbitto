(() => {
  // Drafts on this device (decision 37 (d), docs/architecture/web-javascript.md).
  // Every cleanup has a horizon, a UUIDv7 from PostgreSQL's clock of session
  // ids: the sign-in page renders one on its form, and an organisation page
  // uses its own session. A cleanup removes malformed keys and every entry
  // whose session is strictly older than the horizon, to the millisecond,
  // without reading its value. It runs once per document load; a page
  // restored from the back-forward cache does not run it again.
  const uuid = "[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}";
  // A UUIDv7 starts with 48 bits of milliseconds since the epoch.
  const v7 = "[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}";
  const prefix = "ribbitto.draft.v1:";
  // ribbitto.draft.v1:<member>:<session>:<channel>:<feed|topic>
  const draft = new RegExp("^" + prefix.replaceAll(".", "\\.") + "(" + uuid + "):(" + v7 + "):(" + uuid + "):(feed|" + uuid + ")$");
  const millisecond = (id) => id.slice(0, 8) + id.slice(9, 13);
  const day = 24 * 60 * 60 * 1000;
  // sweep decides every key under the prefix; the sign-out notice is outside
  // it. Only the page's own member and session's values reach keep.
  const sweep = (storage, horizon, member, keep) => {
    // One snapshot of the keys: storage.key(i) can return null while
    // another tab changes the shared storage.
    for (const key of Object.keys(storage)) {
      if (!key.startsWith(prefix)) continue;
      const match = draft.exec(key);
      if (!match) {
        storage.removeItem(key);
      } else if (member && match[2] === horizon) {
        // The page's session with another member is malformed.
        if (match[1] !== member || !keep(storage.getItem(key))) storage.removeItem(key);
      } else if (millisecond(match[2]) < millisecond(horizon)) {
        storage.removeItem(key);
      }
      // Another session's entry in the horizon's millisecond or later stays
      // unread: its order with the horizon is unknown, so it may be newer.
    }
  };
  try {
    const signIn = document.querySelector("form[data-drafts-horizon]")?.dataset.draftsHorizon;
    if (signIn) {
      sweep(window.localStorage, signIn);
      // A fresh horizon on every render, so every write fires a storage event.
      window.localStorage.setItem("ribbitto.signed-out.v1", signIn);
      return;
    }
    const page = document.getElementById("organization-stream")?.dataset;
    const expires = Number(page?.sessionExpires);
    const rendered = Number(page?.renderedAt);
    if (!new RegExp("^" + v7 + "$").test(page?.session) || !Number.isSafeInteger(expires) || !Number.isSafeInteger(rendered)) return;
    const { member, session } = page;
    const storage = window.localStorage;
    const valid = (entry) => {
      const now = Date.now();
      return entry?.member === member && entry.session === session && entry.expires === expires &&
        typeof entry.text === "string" && [...entry.text].length <= 4000 &&
        typeof entry.at === "number" && now - entry.at < 7 * day && entry.at - now <= day;
    };
    sweep(storage, session, member, (value) => {
      try { return valid(JSON.parse(value)); } catch { return false; }
    });
    // The deadline counts from this document's time origin, which precedes
    // the render, so it never comes after the session's expiry; the device
    // clock cannot move performance.now(). setTimeout's delay is capped at
    // 2^31 - 1 ms, under a session's 30 days, so long waits are chained.
    const deadline = expires - rendered;
    let stopped = false, timer, dirty = false, composing = false, at = 0, text = "";
    let lastWrite, source, sending;
    const stop = () => { stopped = true; dirty = false; clearTimeout(timer); markers(); };
    const read = (key) => {
      try { return JSON.parse(storage.getItem(key)); } catch { return null; }
    };
    const form = document.getElementById("message-composer");
    const body = document.getElementById("message-body");
    const key = prefix + member + ":" + session + ":" + form?.dataset.draftChannel + ":" + (form?.dataset.draftTopic || "feed");
    // Keep key scope separate from value validity: foreign values must
    // never be read, even when they claim this page's owner and session.
    const own = (match) => match?.[1] === member && match[2] === session;
    const draftPlace = (match, entry) => valid(entry) && entry.text.trim() ? match.slice(3) : null;
    const marked = (places, channel, topic, open) => !open && places.has(topic === "feed" ? channel : channel + ":" + topic);
    const markers = (root = document.getElementById("sidebar-channels")) => {
      const places = new Set();
      if (!stopped && performance.now() < deadline) {
        try {
          for (const k of Object.keys(storage)) {
            const match = draft.exec(k);
            const place = own(match) ? draftPlace(match, read(k)) : null;
            if (place) { places.add(place[0]); places.add(place.join(":")); }
          }
        } catch { places.clear(); }
      }
      for (const marker of root?.querySelectorAll("[data-draft-marker]") || []) {
        const parts = new URL(marker.closest("a").href).pathname.split("/");
        const channel = parts[4], topic = parts[6] || "feed";
        const open = channel === form?.dataset.draftChannel && topic === (form.dataset.draftTopic || "feed");
        marker.hidden = !marked(places, channel, topic, open);
      }
    };
    markers();
    document.addEventListener("htmx:load", (event) => {
      const root = event.detail.elt;
      if (root.id === "sidebar-channels") markers(root);
      else { const sidebar = root.querySelector("#sidebar-channels"); if (sidebar) markers(sidebar); }
    });
    document.addEventListener("message-sent", () => markers());
    window.addEventListener("pageshow", (event) => { if (event.persisted) markers(); });
    const trim = (limit, except) => {
      const entries = Object.keys(storage).filter((k) => {
        const match = draft.exec(k);
        return k !== except && match?.[1] === member && match[2] === session;
      }).map((k) => [k, read(k)?.at || 0]);
      entries.sort((a, b) => a[1] - b[1] || (a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0));
      for (const [k] of entries.slice(0, Math.max(0, entries.length - limit))) storage.removeItem(k);
    };
    const save = (atSend = false) => {
      clearTimeout(timer);
      if (performance.now() >= deadline) stop();
      if (stopped || (composing && atSend !== true) || !dirty) return;
      dirty = false;
      try {
        // Best effort: another tab may write between this read and setItem,
        // or update an entry after trim selects it. Neither race is repaired.
        if (read(key)?.at >= at) return;
        trim(49, key);
        const value = JSON.stringify({ member, session, expires, text, at });
        storage.setItem(key, value);
        lastWrite = value;
        trim(50);
      } catch {
        // A stored copy may be lost; the textarea and sending remain usable.
      }
    };
    const edited = () => {
      if (stopped) return;
      if (performance.now() >= deadline) { stop(); return; }
      // Snapshot the input, not a later save or a send's textarea cleanup.
      source = null;
      at = Math.max(Date.now(), at + 1);
      text = body.value;
      if (!text.trim() || [...text].length > 4000) text = "";
      dirty = true;
      clearTimeout(timer);
      if (!composing) timer = setTimeout(save, 300);
    };
    if (body && form?.hasAttribute("hx-post") && draft.test(key)) {
      // Another tab may have written since the sweep; check this read too.
      const entry = read(key);
      if (performance.now() < deadline) {
        if (body.value) { at = typeof entry?.at === "number" ? entry.at : 0; edited(); save(); }
        else if (valid(entry)) { at = entry.at; body.value = entry.text; }
      }
      body.addEventListener("message-sending", (event) => {
        const { key: sendKey, body: raw, inPlace } = event.detail;
        if (!sendKey || typeof raw !== "string") return;
        edited();
        text = raw.trim() && [...raw].length <= 4000 ? raw : "";
        source = sendKey;
        sending = { key: sendKey, inPlace };
        at = Math.max(at, (read(key)?.at || 0) + 1);
        save(true);
      });
      const replaceSnapshot = (sendKey, inPlace = false) => {
        const eligible = source === sendKey || (inPlace && source === null);
        // Prefix removal fires no input. Keep the edit time and debounce
        // when another tab's write prevents immediate confirmation.
        if (inPlace && source === null && dirty) {
          text = body.value;
          if (!text.trim() || [...text].length > 4000) text = "";
        }
        try {
          if (!eligible || !lastWrite || storage.getItem(key) !== lastWrite) return;
          edited();
          save();
        } catch { /* Storage errors leave sending usable. */ }
      };
      body.addEventListener("message-sent", (event) => {
        if (sending?.key !== event.detail.key) return;
        const { key: sendKey, inPlace } = sending;
        sending = null;
        // Unsaved pending-path edits keep their timer and cannot be cleared.
        replaceSnapshot(sendKey, inPlace);
      });
      body.addEventListener("message-pending-action", (event) => {
        if (event.detail.kind === "edit") { edited(); save(); }
        else if (event.detail.kind === "remove") replaceSnapshot(event.detail.key);
      });
      body.addEventListener("input", edited);
      body.addEventListener("compositionstart", () => { composing = true; clearTimeout(timer); });
      body.addEventListener("compositionend", () => { composing = false; edited(); });
      window.addEventListener("pagehide", save);
      document.addEventListener("visibilitychange", () => { if (document.hidden) save(); });
    }
    window.addEventListener("storage", (event) => {
      if (event.key === "ribbitto.signed-out.v1") stop();
      else if (event.key === null || event.key.startsWith(prefix)) markers();
    });
    const expire = () => {
      const left = deadline - performance.now();
      if (left > 0) {
        setTimeout(expire, Math.min(left, 2 ** 31 - 1));
        return;
      }
      stop();
      try {
        for (const key of Object.keys(storage)) {
          if (draft.exec(key)?.[2] === session) storage.removeItem(key);
        }
      } catch {
        // Storage became unavailable; a later page's cleanup removes these.
      }
    };
    expire();
  } catch {
    // Without storage there is nothing to clean up; the page still works.
  }
})();
