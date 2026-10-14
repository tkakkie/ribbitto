(() => {
  const states = new WeakMap();
  const initialize = (connection) => {
    if (states.has(connection)) return states.get(connection);
    const form = connection.querySelector("#sidebar-resync");
    if (!form) return;
    const events = new Map();
    let snapshot = BigInt(connection.querySelector("#sidebar-channels").dataset.snapshotSeq);
    let seen = snapshot, lostThrough = 0n;
    let recovering = false, losses = 0, request = null, timer = null, delay = 50;
    const observe = (seq) => { if (seq > seen) seen = seq; };
    const uuid = (value) => typeof value === "string" && /^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/.test(value);
    const sequence = (value) => {
      if (!/^\d+$/.test(value)) throw new Error("invalid snapshot sequence");
      return BigInt(value);
    };
    const schedule = () => {
      if (request || timer || !connection.isConnected) return;
      timer = setTimeout(() => {
        timer = null;
        if (!connection.isConnected) return;
        request = { losses, received: false, success: false };
        form.dispatchEvent(new Event("sidebar-resync", { bubbles: true }));
      }, delay + Math.random() * 25);
    };
    const recover = (through = seen) => {
      if (through > lostThrough) lostThrough = through;
      recovering = true;
      losses++;
      schedule();
    };
    const complete = () => {
      if (!request?.success || !request.received) return;
      // Older snapshots cannot cover dropped events; new losses also require
      // another request, even if this snapshot happens to cover their sequence.
      const covered = request.losses === losses && snapshot >= lostThrough;
      request = null;
      if (covered) {
        recovering = false;
        delay = 50;
        applyRetained();
      } else {
        recovering = true;
        delay = Math.min(delay * 2, 1000);
        schedule();
      }
    };
    const increment = (event) => {
      const hex = (id) => id.replaceAll("-", "");
      const channel = connection.querySelector("#link-channel-" + hex(event.channel));
      const topic = connection.querySelector("#link-topic-" + hex(event.topic)) ||
        (connection.dataset.sidebarTopic === event.topic ? connection.querySelector("#topic-heading-count") : null);
      const badges = [channel, topic].filter(Boolean).map((node) => node.querySelector("[data-unread-count]"));
      // Missing rows are an intended approximation; a shown row missing its
      // server-rendered badge is an application failure.
      for (const badge of badges) {
        if (!badge || !/^\d+$/.test(badge.dataset.unreadCount) ||
            !badge.querySelector("[aria-hidden]") || !badge.querySelector(".sr-only")) throw new Error("invalid badge");
      }
      for (const badge of badges) {
        const count = BigInt(badge.dataset.unreadCount) + 1n;
        const text = count >= 100n ? "99+" : String(count);
        badge.dataset.unreadCount = String(count);
        badge.querySelector("[aria-hidden]").textContent = text;
        const label = badge.querySelector(".sr-only");
        label.textContent = label.textContent.replace(/\d+\+?/, text);
        badge.hidden = false;
      }
    };
    const applyRetained = () => {
      if (recovering) return;
      for (const event of events.values()) {
        if (!event.eligible || event.applied) continue;
        increment(event);
        event.applied = true;
      }
    };
    const unread = (message) => {
      try {
        const event = JSON.parse(message.data);
        if (!event || !Number.isSafeInteger(event.seq) || event.seq < 0) throw new Error("invalid unread sequence");
        const seq = BigInt(event.seq);
        observe(seq);
        if (!uuid(event.channel)) throw new Error("invalid unread");
        // Missing topic/author is legacy, not a new post that can increment.
        if (event.topic === undefined) return;
        if (!uuid(event.topic) || (event.author !== undefined && !uuid(event.author))) throw new Error("invalid routing");
        if (seq <= snapshot || events.has(seq)) return;
        if (events.size === 1000) {
          let dropped = 0n;
          for (const seq of events.keys()) if (BigInt(seq) > dropped) dropped = BigInt(seq);
          events.clear();
          recover(dropped);
        }
        event.eligible = event.author !== undefined && event.author !== connection.dataset.member;
        event.applied = false;
        events.set(seq, event);
        if (!recovering && event.eligible) {
          increment(event);
          event.applied = true;
        }
      } catch (_) {
        recover();
      }
    };
    const sidebar = (event) => {
      event.preventDefault();
      const dispatched = request;
      try {
        const payload = new DOMParser().parseFromString(event.detail.data, "text/html");
        const incoming = payload.getElementById("sidebar-channels");
        const seq = sequence(incoming?.dataset.snapshotSeq);
        observe(seq);
        const previous = connection.querySelector("#sidebar-channels");
        htmx.swap(connection.querySelector("#stream-sink"), event.detail.data, { swapStyle: "none", settleDelay: 0 }, {
          afterSettleCallback: () => {
            try {
              const current = connection.querySelector("#sidebar-channels");
              if (current === previous || sequence(current?.dataset.snapshotSeq) !== seq) throw new Error("snapshot not applied");
              // Validate every shown target, including an unlisted heading.
              for (const root of payload.querySelectorAll("[hx-swap-oob]")) {
                const target = connection.querySelector("#" + root.id);
                if (!target) continue;
                const expected = root.querySelectorAll("[data-unread-count]");
                const actual = target.querySelectorAll("[data-unread-count]");
                if (expected.length !== actual.length || Array.from(expected).some((badge, i) =>
                  !/^\d+$/.test(badge.dataset.unreadCount) || badge.dataset.unreadCount !== actual[i].dataset.unreadCount)) throw new Error("bad snapshot badges");
              }
              snapshot = seq;
              for (const [seq, retained] of events) {
                if (BigInt(seq) <= snapshot) events.delete(seq);
                else retained.applied = false;
              }
              if (request && request === dispatched) request.received = true;
              complete();
              applyRetained();
              if (recovering) schedule();
              htmx.trigger(event.target, "htmx:sseMessage", event.detail);
            } catch (_) {
              if (request && request === dispatched) request.received = true;
              recover();
              complete();
            }
          },
        });
      } catch (_) {
        if (request && request === dispatched) request.received = true;
        recover();
        complete();
      }
    };
    form.addEventListener("htmx:afterRequest", (event) => {
      if (!request) return;
      if (!event.detail.successful) {
        request = null;
        delay = Math.min(delay * 2, 1000);
        recover();
      } else {
        request.success = true;
        try { complete(); }
        catch (_) { recover(); }
      }
    });
    const state = { unread, sidebar, schedule, cleanup: () => clearTimeout(timer) };
    states.set(connection, state);
    return state;
  };
  document.addEventListener("htmx:load", (event) => {
    const root = event.detail.elt;
    if (root.id === "organization-stream") initialize(root);
    for (const connection of root.querySelectorAll("#organization-stream")) initialize(connection);
  });
  document.addEventListener("htmx:sseOpen", (event) => {
    if (event.target.id !== "organization-stream") return;
    const state = initialize(event.target);
    if (state) event.detail.source.addEventListener("unread", state.unread);
  });
  document.addEventListener("htmx:sseBeforeMessage", (event) => {
    if (event.target.id !== "stream-sink" || event.detail.type !== "sidebar") return;
    initialize(event.target.closest("#organization-stream"))?.sidebar(event);
  });
  document.addEventListener("htmx:beforeCleanupElement", (event) => {
    states.get(event.target)?.cleanup();
  });
  const resume = () => {
    const connection = document.getElementById("organization-stream");
    if (connection) initialize(connection)?.schedule();
  };
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") resume();
  });
  window.addEventListener("pageshow", (event) => { if (event.persisted) resume(); });
})();
