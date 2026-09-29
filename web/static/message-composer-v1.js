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
  // htmx does not swap 422 responses by default; only this form opts in.
  document.addEventListener("htmx:beforeSwap", (event) => {
    if (event.detail.target.id === "conversation" && event.detail.xhr.status === 422) {
      event.detail.shouldSwap = true;
      event.detail.isError = false;
    }
  });
  const newest = () => {
    const list = document.getElementById("message-list");
    if (list) list.scrollTop = list.scrollHeight;
  };
  newest();
  document.addEventListener("htmx:afterSettle", (event) => {
    if (event.detail.target.id !== "conversation") return;
    newest();
    document.getElementById("message-body").focus({ preventScroll: true });
  });
})();
