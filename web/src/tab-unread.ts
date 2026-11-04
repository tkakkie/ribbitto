import { getSidebarChannels, LinkChannelDOMPrefix, UnreadCountAttribute, readAttribute } from "./contracts.gen.ts";
import { tabUnread } from "./tab-unread-state.ts";

(() => {
  const icon = document.querySelector<HTMLLinkElement>('link[rel="icon"]')!;
  const plain = icon.href, title = document.title;
  icon.type = "";
  const image = new Image();
  const canvas = document.createElement("canvas");
  canvas.height = 16;
  const paint = canvas.getContext("2d")!;
  const update = () => {
    const state = tabUnread(title, { channels: Array.from(getSidebarChannels()!.querySelectorAll(`[id^=${LinkChannelDOMPrefix}] [${UnreadCountAttribute}]`), node => readAttribute(node, UnreadCountAttribute)!) });
    document.title = state.title;
    if (state.badge && image.width) {
      canvas.width = 16;
      paint.drawImage(image, 0, 0, 16, 16);
      const colors = getComputedStyle(document.documentElement);
      paint.beginPath();
      paint.arc(12, 12, 3, 0, 2 * Math.PI);
      paint.fillStyle = colors.getPropertyValue("--rb-brand");
      paint.strokeStyle = colors.getPropertyValue("--rb-on-brand");
      paint.lineWidth = 2;
      paint.fill();
      paint.stroke();
      icon.href = canvas.toDataURL();
    } else icon.href = plain;
  };
  image.onload = update;
  image.src = plain;
  // Observe the surviving parent: a resync replaces the entire channel list.
  new MutationObserver(update).observe(getSidebarChannels()!.parentElement!, { subtree: true, childList: true, attributes: true, attributeFilter: [UnreadCountAttribute] });
  matchMedia("(prefers-color-scheme: dark)").addEventListener("change", update);
  update();
})();
