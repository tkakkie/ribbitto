import { startSidebarScroll } from "./sidebar-scroll.ts";
import { startReadVisibility } from "./read-visibility.ts";
import { startBranchSelection } from "./branch-selection.ts";
import { startMessageTime } from "./message-time.ts";
import { startMemberMentions } from "./member-mentions.ts";
import { startMentionPicker } from "./mention-picker.ts";
import { startComposer } from "./composer.ts";
import { startPinsSync } from "./pins-sync.ts";
import { startQuoteSelection } from "./quote-selection.ts";
import { startMessageLink } from "./message-link.ts";
import { startMessageHistory } from "./message-history.ts";
import { startSidePanel } from "./side-panel.ts";
import { startSidebarUnread } from "./sidebar-unread.ts";
import { startMessageStream } from "./message-stream.ts";
import { startMessageDrafts } from "./message-drafts.ts";
import { attachPageLifecycle } from "./lifecycle.ts";

/** Starts one ChannelScreen instance; modules retain their own freeze/thaw state. */
export function ChannelScreen() {
  return attachPageLifecycle(window, {
    phase(phase) {
      if (phase !== "start") return;
      startSidebarScroll();
      startReadVisibility();
      startBranchSelection();
      startMessageTime();
      startMemberMentions();
      startMentionPicker();
      startComposer();
      startPinsSync();
      startQuoteSelection();
      startMessageLink();
      startMessageHistory();
      startSidePanel();
      startSidebarUnread();
      startMessageStream();
      startMessageDrafts();
    },
    pageshow() {},
  });
}

ChannelScreen();
