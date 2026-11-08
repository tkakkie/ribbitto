import * as dom from "./contracts.gen.ts";

type BranchControls = HTMLFormControlsCollection & { to: HTMLSelectElement; name: HTMLInputElement };
type LoadEvent = CustomEvent<{ elt: Element }>;
// Keep dataset absence and access semantics while deriving the key from its contract.
const sourceOf = (item: Element): string | undefined => (item as HTMLElement).dataset[dom.SourceAttribute.slice(5)];

export function startBranchSelection() {
  function update() {
    const form = document.getElementById(dom.BranchFormID) as HTMLFormElement | null;
    if (!form) return;
    const items = (Array.from(form.elements) as HTMLInputElement[]).filter(el => el.name === "message");
    const selected = items.find(el => el.checked);
    const source = selected && sourceOf(selected);
    for (const item of items) item.disabled = !!source && sourceOf(item) !== source;
    const to = (form.elements as BranchControls).to;
    for (const option of to.options) option.disabled = !!source && option.value === source;
    if (to.selectedOptions[0]?.disabled) to.value = "";
    (form.elements as BranchControls).name.disabled = to.value !== "";
  }
  document.addEventListener("change", event => {
    if ((event.target as HTMLInputElement).form?.id === dom.BranchFormID) update();
  });
  // Paging and SSE can insert new selectable items while a selection is active.
  document.addEventListener("htmx:load", event => {
    const root = (event as LoadEvent).detail.elt;
    const form = document.getElementById(dom.BranchFormID) as HTMLFormElement | null;
    const selected = form && (Array.from(form.elements) as HTMLInputElement[]).find(el => el.name === "message" && el.checked);
    const source = selected && sourceOf(selected);
    if (!source) return;
    const items = root.matches?.(`input[${dom.SourceAttribute}]`) ? [root] : root.querySelectorAll<HTMLInputElement>(`input[${dom.SourceAttribute}]`);
    for (const item of items) (item as HTMLInputElement).disabled = sourceOf(item) !== source;
  });
  update();
}
