import * as dom from "./contracts.gen.ts";

// Keep each requested form's identity until its response arrives. Parking it
// with its message lets late responses and SSE update it without stealing the
// shared menu's current controls; replacing/pruning that message discards it.
(() => {
  const menu = dom.getMessageActions();
  const template = dom.getMessageActionsTemplate();
  const trigger = dom.getMessageActionsTrigger();
  if (!menu || !template || !trigger) return;
  const enhance = (root: Element) => {
    for (const link of root.querySelectorAll<HTMLAnchorElement>(`a[${dom.MessageActionAttribute}]`)) {
      if (link.nextElementSibling?.matches("button")) continue;
      const button = trigger.content.firstElementChild!.cloneNode(true) as HTMLButtonElement;
      button.className = link.className;
      link.classList.replace("flex", "hidden");
      button.setAttribute("aria-describedby", link.getAttribute("aria-describedby")!);
      link.after(button);
    }
  };
  enhance(document.body);
  document.addEventListener("htmx:load", (event) => enhance((event as CustomEvent<{ elt: Element }>).detail.elt));
  let opener: HTMLButtonElement | null = null;
  const park = () => {
    const form = menu.querySelector<HTMLFormElement>(`form[${dom.PinControlAttribute}]`);
    if (!form) return;
    form.hidden = true;
    if (opener?.isConnected) opener.nextElementSibling!.append(form);
    else form.remove();
  };
  document.addEventListener("click", (event) => {
    const button = event.target instanceof Element
      ? event.target.closest<HTMLButtonElement>(`button[popovertarget="${dom.MessageActionsID}"]`) : null;
    if (!button) return;
    if (opener === button && menu.matches(":popover-open")) return;
    if (menu.matches(":popover-open")) menu.hidePopover();
    park();
    opener = button;
    const link = button.previousElementSibling as HTMLAnchorElement;
    const id = dom.readAttribute(link, dom.MessageActionAttribute)!;
    const suffix = id.replaceAll("-", "");
    let form = dom.getPinFormEntry(suffix);
    if (!form) {
      form = template.content.firstElementChild!.cloneNode(true) as HTMLFormElement;
      form.id = dom.pinFormEntryID(suffix);
      dom.writeAttribute(form, dom.PinControlAttribute, id);
      const pinned = dom.readAttribute(link, dom.PinnedAttribute)!;
      form.action = dom.readAttribute(link, pinned === "true" ? dom.UnpinURLAttribute : dom.PinURLAttribute)!;
      dom.writeAttribute(form, dom.PinnedAttribute, pinned);
      for (const action of form.querySelectorAll<HTMLButtonElement>(`[${dom.PinActionAttribute}]`)) {
        const state = dom.readAttribute(action, dom.PinActionAttribute)!;
        action.id = state === "true" ? dom.pinTrueEntryID(suffix) : dom.pinFalseEntryID(suffix);
        action.formAction = dom.readAttribute(link, state === "true" ? dom.UnpinURLAttribute : dom.PinURLAttribute)!;
        action.hidden = state !== pinned;
      }
    }
    menu.querySelector<HTMLAnchorElement>(`[${dom.CopyLinkAttribute}]`)!.href = dom.readAttribute(link, dom.MessageURLAttribute)!;
    menu.querySelector<HTMLButtonElement>(`[form="${dom.QuoteFormID}"]`)!.value = id;
    const resolve = dom.getResolutionForm(menu)!;
    resolve.action = dom.readAttribute(link, dom.ResolveURLAttribute)!;
    resolve.hidden = !dom.readAttribute(link, dom.ResolveURLAttribute);
    resolve.querySelector<HTMLButtonElement>("button")!.value = id;
    form.hidden = false;
    menu.append(form);
    window.htmx?.trigger(form, "htmx:load", { elt: form });
  }, true);
  menu.addEventListener("toggle", () => {
    if (!menu.matches(":popover-open")) park();
  });
  const dismiss = () => {
    const active = document.activeElement;
    const restore = active === opener || menu.contains(active) || (menu.matches(":popover-open") && active === document.body);
    if (menu.matches(":popover-open")) menu.hidePopover();
    park();
    opener = null;
    // The removed trigger cannot receive native popover focus restoration.
    if (restore) dom.getMessageList()?.focus({ preventScroll: true });
  };
  const list = dom.getMessageList();
  if (list) new MutationObserver(() => {
    if (opener && !opener.isConnected) dismiss();
  }).observe(list, { childList: true, subtree: true });
  document.addEventListener("htmx:beforeCleanupElement", (event) => {
    const root = (event as CustomEvent<{ elt: Element }>).detail.elt;
    if (opener && root.contains(opener)) dismiss();
  });
})();
