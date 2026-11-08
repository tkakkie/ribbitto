import { startModule } from "./start-module.ts";
import { startSidebarScroll } from "./sidebar-scroll.ts";
import { startSidebarUnread } from "./sidebar-unread.ts";
import { startMessageStream } from "./message-stream.ts";
import { startMessageDrafts } from "./message-drafts.ts";
import { attachPageLifecycle } from "./lifecycle.ts";

/** Starts one ChannelListScreen instance; modules retain their own freeze/thaw state. */
export function ChannelListScreen() {
  return attachPageLifecycle(window, {
    phase(phase) {
      if (phase !== "start") return;
      startModule(startSidebarScroll);
      startModule(startSidebarUnread);
      startModule(startMessageStream);
      startModule(startMessageDrafts);
    },
    pageshow() {},
  });
}

ChannelListScreen();
