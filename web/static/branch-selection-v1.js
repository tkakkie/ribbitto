(() => {
  function update() {
    const form = document.getElementById("branch-form");
    if (!form) return;
    const items = Array.from(form.elements).filter(el => el.name === "message");
    const source = items.find(el => el.checked)?.dataset.source;
    for (const item of items) item.disabled = !!source && item.dataset.source !== source;
    const to = form.elements.to;
    for (const option of to.options) option.disabled = !!source && option.value === source;
    if (to.selectedOptions[0]?.disabled) to.value = "";
    form.elements.name.disabled = to.value !== "";
  }
  document.addEventListener("change", event => {
    if (event.target.form?.id === "branch-form") update();
  });
  // Paging and SSE can insert new selectable items while a selection is active.
  document.addEventListener("htmx:load", event => {
    const root = event.detail.elt;
    const form = document.getElementById("branch-form");
    const source = form && Array.from(form.elements).find(el => el.name === "message" && el.checked)?.dataset.source;
    if (!source) return;
    const items = root.matches?.("input[data-source]") ? [root] : root.querySelectorAll("input[data-source]");
    for (const item of items) item.disabled = item.dataset.source !== source;
  });
  update();
})();
