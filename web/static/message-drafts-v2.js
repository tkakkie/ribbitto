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
  const draft = new RegExp("^" + prefix.replaceAll(".", "\\.") + "(" + uuid + "):(" + v7 + "):" + uuid + ":(?:feed|" + uuid + ")$");
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
    sweep(storage, session, member, (value) => {
      let entry;
      try {
        entry = JSON.parse(value);
      } catch {
        return false;
      }
      const now = Date.now();
      return entry?.member === member && entry.session === session && entry.expires === expires &&
        typeof entry.text === "string" && [...entry.text].length <= 4000 &&
        typeof entry.at === "number" && now - entry.at < 7 * day && entry.at - now <= day;
    });
    // The deadline counts from this document's time origin, which precedes
    // the render, so it never comes after the session's expiry; the device
    // clock cannot move performance.now(). setTimeout's delay is capped at
    // 2^31 - 1 ms, under a session's 30 days, so long waits are chained.
    const deadline = expires - rendered;
    const expire = () => {
      const left = deadline - performance.now();
      if (left > 0) {
        setTimeout(expire, Math.min(left, 2 ** 31 - 1));
        return;
      }
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
