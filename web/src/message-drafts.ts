// Adapter for local message drafts (decision 37 (d)).

import {
  DraftChannelAttribute, DraftMarkerAttribute, DraftTopicAttribute, DraftsHorizonAttribute,
  MemberAttribute, RenderedAtAttribute, SessionAttribute, SessionExpiresAttribute, SidebarChannelsID,
  dispatchMessageDraftRestoredEvent, getMessageBody, getMessageComposer, getOrganizationStream, getSidebarChannels,
  listenMessagePendingActionEvent, listenMessageSendingEvent, listenMessageSentEvent, readAttribute,
} from "./contracts.gen.ts";
import {
  type ExpiryState, type SendingSnapshot, DraftPrefix, SignedOutNoticeKey,
  canReplaceSnapshot, checkSessionExpired, collectDraftPlaces, draftKey, isSendDetail, isSessionDraftKey, isValidDraftEntry,
  marked, matchSentSnapshot, nextEditTime, nextExpiryDelay, normalizeDraftText, parseDraftEntry,
  parseDraftKey, parsePageScope, parseSidebarPlace, pendingActionDecision, prepareSendSnapshot,
  restoredSendKey, selectKeysToTrim, shouldWriteDraft, sweepKeys,
} from "./drafts.ts";

try {
  const signInForm = document.querySelector(`form[${DraftsHorizonAttribute}]`);
  const signIn = signInForm ? readAttribute(signInForm, DraftsHorizonAttribute) : null;
  if (signIn) {
    const storage = window.localStorage;
    sweepKeys(storage, signIn);
    storage.setItem(SignedOutNoticeKey, signIn);
  } else {
    const stream = getOrganizationStream();
    const scope = stream ? parsePageScope({
      member: readAttribute(stream, MemberAttribute),
      session: readAttribute(stream, SessionAttribute),
      expires: readAttribute(stream, SessionExpiresAttribute),
      rendered: readAttribute(stream, RenderedAtAttribute),
    }) : null;
    if (scope) {
      const { member, session, expires, deadline } = scope;
      const storage = window.localStorage;
      sweepKeys(storage, session, scope, Date.now);
      const expiryState: ExpiryState = { expired: false, deadline, expires };
      let stopped = false, timer: ReturnType<typeof setTimeout> | undefined, dirty = false, composing = false, at = 0, text = "";
      let lastWrite: string | undefined, source: string | null | undefined, sending: SendingSnapshot | null = null, savedKey: string | null = null;
      const stop = (): void => { stopped = true; dirty = false; clearTimeout(timer); markers(); };
      const read = (k: string): Record<string, unknown> | null => {
        try { return parseDraftEntry(storage.getItem(k)) as Record<string, unknown> | null; } catch { return null; }
      };
      const form = getMessageComposer();
      const body = getMessageBody();
      const openChannel = form ? (readAttribute(form, DraftChannelAttribute) ?? undefined) : undefined;
      const openTopic = (form ? readAttribute(form, DraftTopicAttribute) : null) || "feed";
      const key = draftKey(member, session, openChannel, openTopic);
      const sessionExpired = (): boolean => {
        const status = checkSessionExpired(expiryState, performance.now(), Date.now());
        if (status.justExpired) {
          stop();
          try {
            for (const k of Object.keys(storage)) if (isSessionDraftKey(k, session)) storage.removeItem(k);
          } catch { /* Storage became unavailable; a later page's cleanup removes these. */ }
        }
        return status.expired;
      };
      const markers = (root: Element | null = getSidebarChannels()): void => {
        if (!root) return;
        let places = new Set<string>();
        if (!sessionExpired() && !stopped) {
          try {
            places = collectDraftPlaces(Object.keys(storage), scope, Date.now, (k) => {
              try { return storage.getItem(k); } catch { return null; }
            });
          } catch { places.clear(); }
        }
        for (const marker of root.querySelectorAll<HTMLElement>(`[${DraftMarkerAttribute}]`)) {
          const anchor = marker.closest("a");
          if (!anchor) continue;
          const { channel, topic } = parseSidebarPlace(anchor.href);
          marker.hidden = !marked(places, channel, topic, channel === openChannel && topic === openTopic);
        }
      };
      markers();
      document.addEventListener("htmx:load", (event: Event) => {
        const root = (event as CustomEvent<{ elt?: Element | null }>).detail?.elt;
        if (!root) return;
        if (root.id === SidebarChannelsID) markers(root);
        else { const sidebar = getSidebarChannels(root); if (sidebar) markers(sidebar); }
      });
      listenMessageSentEvent(document, () => markers());
      window.addEventListener("pageshow", (event: PageTransitionEvent) => { if (event.persisted) markers(); });
      const trim = (limit: number, except?: string): void => {
        for (const k of selectKeysToTrim(Object.keys(storage), scope, limit, (k) => read(k)?.at, except)) storage.removeItem(k);
      };
      const save = (atSend: unknown = false): void => {
        clearTimeout(timer);
        if (sessionExpired() || stopped || (composing && atSend !== true) || !dirty) return;
        dirty = false;
        try {
          if (!shouldWriteDraft(read(key)?.at, at)) return;
          trim(49, key);
          const value = JSON.stringify({ member, session, expires, text, at, key: savedKey });
          storage.setItem(key, value);
          lastWrite = value;
          trim(50);
        } catch { /* A stored copy may be lost; the textarea and sending remain usable. */ }
      };
      const edited = (): void => {
        if (sessionExpired() || stopped || !body) return;
        source = null;
        savedKey = null;
        at = nextEditTime(at, Date.now());
        text = normalizeDraftText(body.value);
        dirty = true;
        clearTimeout(timer);
        if (!composing) timer = setTimeout(save, 300);
      };
      if (body && form?.hasAttribute("hx-post") && parseDraftKey(key)) {
        const entry = read(key);
        if (!sessionExpired()) {
          if (body.value) { at = typeof entry?.at === "number" ? entry.at : 0; edited(); save(); }
          else if (isValidDraftEntry(entry, scope, Date.now())) {
            at = entry.at;
            body.value = entry.text;
            const restored = restoredSendKey(entry);
            if (restored) {
              savedKey = restored;
              source = restored;
              lastWrite = storage.getItem(key) ?? undefined;
              dispatchMessageDraftRestoredEvent(body, { key: restored });
            }
          }
        }
        listenMessageSendingEvent(body, (detail) => {
          if (!isSendDetail(detail)) return;
          edited();
          const snap = prepareSendSnapshot(detail, at, read(key)?.at);
          text = snap.text;
          source = snap.source;
          savedKey = snap.savedKey;
          sending = snap.sending;
          at = snap.at;
          save(true);
        });
        const replaceSnapshot = (sendKey: string, inPlace = false): void => {
          if (inPlace && source === null && dirty) text = normalizeDraftText(body.value);
          try {
            if (!canReplaceSnapshot(source, sendKey, inPlace, lastWrite, storage.getItem(key))) return;
            edited();
            save();
          } catch { /* Storage errors leave sending usable. */ }
        };
        listenMessageSentEvent(body, (detail) => {
          const matched = matchSentSnapshot(sending, detail?.key);
          if (!matched) return;
          sending = null;
          replaceSnapshot(matched.key, matched.inPlace);
        });
        listenMessagePendingActionEvent(body, (detail) => {
          const decision = pendingActionDecision(detail);
          if (decision.action === "save-edit") { edited(); savedKey = decision.savedKey; save(); }
          else if (decision.action === "replace") replaceSnapshot(decision.sendKey);
        });
        body.addEventListener("input", edited);
        body.addEventListener("compositionstart", () => { composing = true; clearTimeout(timer); });
        body.addEventListener("compositionend", () => { composing = false; edited(); });
        window.addEventListener("pagehide", () => save());
        document.addEventListener("visibilitychange", () => { if (document.hidden) save(); });
      }
      window.addEventListener("storage", (event: StorageEvent) => {
        if (event.key === SignedOutNoticeKey) stop();
        else if (event.key === null || event.key.startsWith(DraftPrefix)) markers();
      });
      const expire = (): void => {
        if (!sessionExpired()) setTimeout(expire, nextExpiryDelay(deadline, performance.now()));
      };
      expire();
    }
  }
} catch { /* Without storage there is nothing to clean up; the page still works. */ }
