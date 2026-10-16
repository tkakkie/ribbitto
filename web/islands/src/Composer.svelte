<script>
  import { onMount } from "svelte";

  // Prototype instrumentation for #1152's leak probe: one marker per live
  // component, named in a heap snapshot (the build keeps names).
  class ComposerIslandMarker {}

  // Everything the textarea shows comes from the server-rendered textarea the
  // island replaces: its attributes, its text and its invalid state. Labels,
  // help and errors stay server-rendered outside the island (decision 19).
  let { id, name, rows, dir, className, describedBy, invalid: initiallyInvalid, value } = $props();

  // svelte-ignore state_referenced_locally
  let invalid = $state.raw(initiallyInvalid);
  let mounted = $state(false);
  let body;
  const marker = new ComposerIslandMarker();

  // Some phone IMEs end composition before dispatching the confirming Enter,
  // with neither isComposing nor 229. Keep that short event sequence local to
  // its textarea; a later deliberate Enter still sends normally.
  let compositionEnded;
  // A post's success may arrive mid-composition, when rewriting the value
  // would break the composition: the sent text then waits for its end.
  let composing = false;
  let pendingRemoval;

  // Only the sent text leaves, and only if the textarea still starts with it.
  // setRangeText keeps the caret and selection on the remaining text and
  // fires no input event, so no typing signal follows.
  const removeSent = (sent) => {
    if (sent && body.value.startsWith(sent)) body.setRangeText("", 0, sent.length, "preserve");
  };

  const command = (event) => {
    if (event.isComposing || event.keyCode === 229 || event.key !== "Enter") return;
    if (compositionEnded !== undefined && event.timeStamp - compositionEnded < 100) return;
    return event.shiftKey ? "newline" : "send";
  };

  const keydown = (event) => {
    if (command(event) !== "send") return;
    event.preventDefault();
    if (!event.repeat && !body.disabled) body.form.requestSubmit();
  };

  const signal = (event) => {
    if (event.isComposing) return;
    const form = document.getElementById("typing-form");
    if (form && body.value.trim()) htmx.trigger(form, "typing-input");
  };

  const compositionstart = () => {
    composing = true;
  };

  const compositionend = (event) => {
    compositionEnded = event.timeStamp;
    composing = false;
    if (pendingRemoval !== undefined) {
      removeSent(pendingRemoval);
      pendingRemoval = undefined;
    }
    signal(event);
  };

  onMount(() => {
    // Capturing, so the shown cursor is in place before htmx reads the form.
    const submit = (event) => {
      if (event.target !== body.form) return;
      const stream = document.getElementById("organization-stream");
      const cursor = body.form.elements.cursor;
      if (stream && cursor) cursor.value = stream.dataset.eventCursor;
    };
    // Responses replace only #message-composer-state (#1173): the island and
    // its textarea stay in place while a post is in flight.
    const afterSwap = (event) => {
      const state = event.target;
      if (state.id !== "message-composer-state") return;
      invalid = state.hasAttribute("data-invalid");
      if (event.detail.xhr.status === 200) {
        const sent = event.detail.requestConfig.formData.get("body");
        if (composing) pendingRemoval = sent;
        else removeSent(sent);
      }
      // Focus on the send button returns to the textarea. htmx disables the
      // button in flight, and Chrome then moves its focus to the body at the
      // next frame.
      const active = document.activeElement;
      if (active === body.form.querySelector("button") || active === document.body) body.focus({ preventScroll: true });
    };
    document.addEventListener("submit", submit, true);
    document.addEventListener("htmx:afterSwap", afterSwap);
    performance.mark("island:mount");
    return () => {
      document.removeEventListener("submit", submit, true);
      document.removeEventListener("htmx:afterSwap", afterSwap);
      performance.mark("island:destroy", { detail: marker.constructor.name });
    };
  });

  // Declared last: #1152's initial-JavaScript endpoint follows every effect.
  onMount(() => {
    mounted = true;
    performance.mark("island:mounted");
  });
</script>

<textarea
  bind:this={body}
  {id}
  {name}
  {rows}
  {dir}
  class={className}
  aria-describedby={describedBy}
  aria-invalid={invalid ? "true" : "false"}
  data-island={mounted ? "mounted" : "mounting"}
  {value}
  onkeydown={keydown}
  oninput={signal}
  oncompositionstart={compositionstart}
  oncompositionend={compositionend}
></textarea>
