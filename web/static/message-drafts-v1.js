(() => {
  // Drafts on this device (decision 37 (d), docs/architecture/web-javascript.md).
  // On a signed-out sign-in page the server renders a horizon, a UUIDv7 from
  // PostgreSQL's clock of session ids. This removes every stored draft whose
  // session is strictly older than it, to the millisecond, then writes the
  // sign-out notice for other open tabs. It runs once per document load; a
  // page restored from the back-forward cache does not run it again.
  const horizon = document.querySelector("form[data-drafts-horizon]")?.dataset.draftsHorizon;
  if (!horizon) return;
  const uuid = "[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}";
  // A UUIDv7 starts with 48 bits of milliseconds since the epoch.
  const v7 = "[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}";
  const prefix = "ribbitto.draft.v1:";
  // ribbitto.draft.v1:<member>:<session>:<channel>:<feed|topic>
  const draft = new RegExp("^" + prefix.replaceAll(".", "\\.") + uuid + ":(" + v7 + "):" + uuid + ":(?:feed|" + uuid + ")$");
  const millisecond = (id) => id.slice(0, 8) + id.slice(9, 13);
  try {
    const storage = window.localStorage;
    const keys = [];
    for (let i = 0; i < storage.length; i++) keys.push(storage.key(i));
    for (const key of keys) {
      if (!key.startsWith(prefix)) continue;
      const match = draft.exec(key);
      // Only the key is read, never another session's value. An entry in
      // the horizon's millisecond stays: its order with the horizon is
      // unknown, so it may belong to a newer session.
      if (!match || millisecond(match[1]) < millisecond(horizon)) storage.removeItem(key);
    }
    // A fresh horizon on every render, so every write fires a storage event.
    storage.setItem("ribbitto.signed-out.v1", horizon);
  } catch {
    // Without storage there is nothing to clean up; the page still works.
  }
})();
