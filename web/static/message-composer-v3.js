(() => {
  const command = (event) => {
    if (event.isComposing || event.keyCode === 229 || event.key !== "Enter") return;
    return event.shiftKey ? "newline" : "send";
  };
  const submit = (form) => form.requestSubmit();
  document.addEventListener("keydown", (event) => {
    if (event.target.id !== "message-body" || command(event) !== "send") return;
    event.preventDefault();
    if (!event.repeat && !event.target.disabled) submit(event.target.form);
  });
  const newest = () => {
    const list = document.getElementById("message-list");
    if (list) list.scrollTop = list.scrollHeight;
  };
  newest();
  document.addEventListener("htmx:afterSettle", (event) => {
    if (event.detail.target?.id !== "message-composer") return;
    document.getElementById("message-body").focus({ preventScroll: true });
  });
})();
