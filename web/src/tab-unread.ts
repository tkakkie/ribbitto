import { getSidebarChannels, LinkChannelDOMPrefix, UnreadCountAttribute, readAttribute } from "./contracts.gen.ts";
import { tabUnread } from "./tab-unread-state.ts";

export function startTabUnread() {
  const icon = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
  if (!icon || !getSidebarChannels()) return;
  const plain = icon.href, title = document.title, image = new Image(), canvas = document.createElement("canvas"), paint = canvas.getContext("2d")!;
  let loaded = false;
  const update = () => {
    const prefix = tabUnread(Array.from(getSidebarChannels()!.querySelectorAll(`[id^=${LinkChannelDOMPrefix}] [${UnreadCountAttribute}]`), node => readAttribute(node, UnreadCountAttribute)!));
    let href = plain;
    document.title = prefix + title;
    if (prefix && loaded) {
      canvas.width = canvas.height = 16;
      paint.drawImage(image, 0, 0, 16, 16);
      const colors = getComputedStyle(document.documentElement);
      paint.beginPath();
      paint.arc(12, 12, 3, 0, 7);
      paint.fillStyle = colors.getPropertyValue("--rb-brand");
      paint.strokeStyle = colors.getPropertyValue("--rb-on-brand");
      paint.lineWidth = 2;
      paint.fill();
      paint.stroke();
      href = canvas.toDataURL();
    }
    icon.href = href;
  };
  icon.type = "";
  image.onload = () => {
    loaded = true;
    update();
  };
  image.src = plain;
  // Observe the surviving parent: a resync replaces the entire channel list.
  new MutationObserver(update).observe(getSidebarChannels()!.parentElement!, { subtree: true, childList: true, attributeFilter: [UnreadCountAttribute] });
  matchMedia("(prefers-color-scheme: dark)").addEventListener("change", update);
  update();
}
