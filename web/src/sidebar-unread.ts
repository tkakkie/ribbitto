import {
  getSidebarResync, getSidebarChannels, getChannelLink, getTopicLink, getTopicHeadingCount, getStreamSink,
  getOrganizationStream, organizationStreamSelector, OrganizationStreamID, StreamSinkID, SidebarChannelsID,
  SidebarResyncEvent, MemberAttribute, SidebarTopicAttribute, SnapshotSeqAttribute, UnreadCountAttribute,
  readAttribute, writeAttribute, type ContractAttribute,
} from "./contracts.gen.ts";
import { unreadState, observe, sequence, receiveFrame, retain, loss,
  failedRequest, retryWait, resumeWait, complete as completeResync, snapshot, applicable, markApplied, incrementCount, incrementLabel, badgeFailure, missingRow, snapshotBadgesMatch, mayRequest,
  type UnreadFrame } from "./unread-counts.ts";
type SidebarEvent = CustomEvent<{ data: string; type: string }>;
const attr = (node: Element, name: ContractAttribute) => readAttribute(node, name) ?? undefined;

(() => {
  const states = new WeakMap<Element, { unread: (message: MessageEvent) => void; sidebar: (event: SidebarEvent) => void; schedule: (wait?: number) => void; cleanup: () => void }>();
  const initialize = (connection: Element) => {
    if (states.has(connection)) return states.get(connection);
    const form = getSidebarResync(connection);
    if (!form) return;
    let state = unreadState(BigInt(attr(getSidebarChannels(connection)!, SnapshotSeqAttribute)!));
    let timer: ReturnType<typeof setTimeout> | null = null;
    const schedule = (wait = retryWait(state.delay, Math.random())) => {
      if (!mayRequest(state, !!timer) || !mayRequest(state, !!timer, connection.isConnected)) return;
      timer = setTimeout(() => {
        timer = null;
        if (!connection.isConnected) return;
        state.request = { losses: state.losses, received: false, success: false };
        form.dispatchEvent(new Event(SidebarResyncEvent, { bubbles: true }));
      }, wait);
    };
    const recover = (through = state.seen) => {
      state = loss(state, through);
      schedule();
    };
    const complete = () => {
      const result = completeResync(state);
      state = result.state;
      const covered = result.covered;
      if (covered === true) applyRetained();
      else if (covered === false) schedule();
    };
    const increment = (event: UnreadFrame) => {
      const hex = (id: string) => id.replaceAll("-", "");
      const channel = getChannelLink(hex(event.channel), connection);
      const topic = getTopicLink(hex(event.topic), connection) ||
        (attr(connection, SidebarTopicAttribute) === event.topic ? getTopicHeadingCount(connection) : null);
      const badges = [channel, topic].filter((node) => !missingRow(node)).map((node) => node.querySelector<HTMLElement>(`[${UnreadCountAttribute}]`));
      // Missing rows are an intended approximation; a shown row missing its
      // server-rendered badge is an application failure.
      for (const badge of badges) {
        const raw = badge ? attr(badge, UnreadCountAttribute) : undefined;
        if (badgeFailure(raw) || badgeFailure(raw, !!badge!.querySelector("[aria-hidden]")) ||
            badgeFailure(raw, true, !!badge!.querySelector(".sr-only"))) throw new Error("invalid badge");
      }
      for (const badge of badges) {
        const { count, text } = incrementCount(attr(badge!, UnreadCountAttribute)!);
        writeAttribute(badge!, UnreadCountAttribute, count);
        badge!.querySelector("[aria-hidden]")!.textContent = text;
        const label = badge!.querySelector(".sr-only")!;
        label.textContent = incrementLabel(label.textContent!, text);
        badge!.hidden = false;
      }
    };
    const applyRetained = () => {
      if (state.recovering) return;
      for (const [seq, event] of state.events) {
        if (!applicable(state, event)) continue;
        increment(event);
        state = markApplied(state, seq);
      }
    };
    const unread = (message: MessageEvent) => {
      try {
        const event: unknown = JSON.parse(message.data);
        const result = receiveFrame(state, event);
        state = result.state;
        if (result.action === "ignore") return;
        if (result.action === "retain") {
          if (result.overflowed) schedule();
          const seq = result.seq;
          const frame = event as UnreadFrame;
          state = retain(state, seq, frame, frame.author === undefined ? undefined : attr(connection, MemberAttribute));
          const retained = state.events.get(seq)!;
          if (applicable(state, retained)) {
            increment(frame);
            state = markApplied(state, seq);
          }
          return;
        }
      } catch (_) {
        recover();
        return;
      }
      // A malformed frame recovered in the catch before extraction: timer errors escape.
      schedule();
    };
    const sidebar = (event: SidebarEvent) => {
      event.preventDefault();
      const dispatched = state.request;
      try {
        const payload = new DOMParser().parseFromString(event.detail.data, "text/html");
        const incoming = payload.getElementById(SidebarChannelsID);
        const seq = sequence(incoming ? attr(incoming, SnapshotSeqAttribute) : undefined);
        state = observe(state, seq);
        const previous = getSidebarChannels(connection);
        htmx.swap(getStreamSink(connection)!, event.detail.data, { swapStyle: "none", settleDelay: 0 }, {
          afterSettleCallback: () => {
            try {
              const current = getSidebarChannels(connection);
              if (current === previous || sequence(current ? attr(current, SnapshotSeqAttribute) : undefined) !== seq) throw new Error("snapshot not applied");
              // Validate every shown target, including an unlisted heading.
              for (const root of payload.querySelectorAll("[hx-swap-oob]")) {
                const target = connection.querySelector("#" + root.id);
                if (!target) continue;
                const expected = root.querySelectorAll(`[${UnreadCountAttribute}]`);
                const actual = target.querySelectorAll(`[${UnreadCountAttribute}]`);
                const expectedLength = expected.length, actualLength = actual.length;
                if (!snapshotBadgesMatch(expectedLength, actualLength) || Array.from(expected).some((badge, i) =>
                  badgeFailure(attr(badge, UnreadCountAttribute)) || !snapshotBadgesMatch(expectedLength, actualLength,
                    attr(badge, UnreadCountAttribute), attr(actual[i], UnreadCountAttribute)))) throw new Error("bad snapshot badges");
              }
              state = snapshot(state, seq);
              if (state.request && state.request === dispatched) state.request.received = true;
              complete();
              applyRetained();
              if (state.recovering) schedule();
              htmx.trigger(event.target as Element, "htmx:sseMessage", event.detail);
            } catch (_) {
              if (state.request && state.request === dispatched) state.request.received = true;
              recover();
              complete();
            }
          },
        });
      } catch (_) {
        if (state.request && state.request === dispatched) state.request.received = true;
        recover();
        complete();
      }
    };
    form.addEventListener("htmx:afterRequest", (native) => {
      const event = native as CustomEvent<{ successful: boolean }>;
      if (!state.request) return;
      if (!event.detail.successful) {
        state = failedRequest(state);
        schedule();
      } else {
        state.request.success = true;
        try { complete(); }
        catch (_) { recover(); }
      }
    });
    // The native API accepts null; preserve the cleanup call even without a timer.
    const adapter = { unread, sidebar, schedule, cleanup: () => { (clearTimeout as (handle: number | null) => void)(timer); timer = null; } };
    states.set(connection, adapter);
    return adapter;
  };
  document.addEventListener("htmx:load", (event) => {
    const root = (event as CustomEvent<{ elt: Element }>).detail.elt;
    if (root.id === OrganizationStreamID) initialize(root);
    for (const connection of root.querySelectorAll(organizationStreamSelector())) initialize(connection);
  });
  document.addEventListener("htmx:sseOpen", (event) => {
    const target = event.target as Element;
    if (target.id !== OrganizationStreamID) return;
    const state = initialize(target);
    if (state) (event as CustomEvent<{ source: EventTarget }>).detail.source.addEventListener("unread", state.unread as EventListener);
  });
  document.addEventListener("htmx:sseBeforeMessage", (event) => {
    const target = event.target as Element;
    if (target.id !== StreamSinkID || (event as SidebarEvent).detail.type !== "sidebar") return;
    initialize(target.closest(organizationStreamSelector())!)?.sidebar(event as SidebarEvent);
  });
  document.addEventListener("htmx:beforeCleanupElement", (event) => {
    states.get(event.target as Element)?.cleanup();
  });
  const resume = () => {
    const connection = getOrganizationStream();
    if (connection) initialize(connection)?.schedule(resumeWait(Math.random()));
  };
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") resume();
  });
  window.addEventListener("pageshow", (event) => { if (event.persisted) resume(); });
})();
