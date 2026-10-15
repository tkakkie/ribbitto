// Visibility and DOM application are UX glue; htmx owns every request.
(() => {
  const initialized = new WeakSet();
  let inFlight = false;
  let active = null;
  document.addEventListener('htmx:load', event => {
    const root = event.detail.elt;
    const form = root.id === 'read-form' ? root : root.querySelector('#read-form');
    if (!form || initialized.has(form)) return;
    initialized.add(form);
    const stream = document.getElementById('organization-stream');
    const snapshot = form.elements.cursor.value;
    let attempted = null;
    let succeeded = null;
    let timer = null;
    let failures = 0;
    let navigating = false;
    // Mark as read's cursor until a request covering it succeeds: one action
    // reads everything shown, and its 429 retries pass a covering panel.
    let intent = null;
    const needed = () => !navigating && document.visibilityState === 'visible' && form.isConnected;
    // A modal panel covers the conversation (an overlay, or a phone's sheet):
    // automatic reading waits, Mark as read never does. side-panel-v3.js
    // alone decides it and has set aria-modal before this reads it, on load
    // and on resize (that script's comment says why).
    const covered = () => document.getElementById('side-panel')?.getAttribute('aria-modal') === 'true';
    const clearRetry = () => {
      clearTimeout(timer);
      timer = null;
    };
    const latest = () => stream.dataset.appliedCursor || snapshot;
    const cursor = () => intent ?? (attempted === null ? snapshot : latest());
    const visible = () => {
      if (!needed() || (intent === null && covered()) || inFlight || timer !== null) return;
      if (succeeded === null || BigInt(cursor()) > succeeded) htmx.trigger(form, 'read-visible');
      else intent = null;
    };
    form.addEventListener('htmx:configRequest', event => {
      if (event.detail.triggeringEvent?.type === 'submit') intent = latest();
      form.elements.cursor.value = cursor();
      event.detail.parameters.cursor = cursor();
    });
    form.addEventListener('htmx:beforeRequest', event => {
      // Mark as read is the newest intent: it replaces a pending retry rather
      // than waiting behind it, since leaving the page would drop that retry.
      if (event.detail.requestConfig?.triggeringEvent?.type === 'submit' && needed() && !inFlight) clearRetry();
      if (!needed() || inFlight || timer !== null) {
        event.preventDefault();
        return;
      }
      inFlight = true;
      attempted = BigInt(cursor());
    });
    form.addEventListener('htmx:afterRequest', event => {
      inFlight = false;
      // A replacement waits for the old form's request slot to be released.
      if (!form.isConnected) {
        queueMicrotask(() => active?.());
        return;
      }
      if (event.detail.successful) {
        succeeded = attempted;
        failures = 0;
        if (intent !== null && attempted >= BigInt(intent)) intent = null;
      } else if (event.detail.xhr.status === 429) {
        if (!needed()) return;
        const header = event.detail.xhr.getResponseHeader('Retry-After');
        const seconds = header === null ? NaN : Number(header);
        const after = Number.isFinite(seconds) ? seconds * 1000 : Date.parse(header) - Date.now();
        // The initial cap bounds our backoff; a server's longer minimum wins.
        const cap = Math.min(8000, 1000 * 2 ** Math.min(failures++, 3));
        // setTimeout treats a delay above 2^31 - 1 ms as zero, which would
        // retry at once instead of honouring a very long Retry-After.
        const delay = Math.min(Math.max(cap * (0.5 + Math.random() / 2), after || 0), 2147483647);
        clearRetry();
        timer = setTimeout(() => {
          timer = null;
          visible();
        }, delay);
        return;
      }
      else {
        // A failed Mark as read waits for another click. Flush newer shown
        // work without retrying the same cursor.
        intent = null;
        if (BigInt(cursor()) <= attempted) return;
      }
      // htmx releases its request lock after afterRequest returns. Read the
      // newest cursor then, rather than queueing snapshots of older triggers.
      queueMicrotask(() => active?.());
    });
    const visibility = () => {
      if (!needed()) clearRetry();
      else visible();
    };
    const navigation = () => {
      navigating = true;
      clearRetry();
    };
    const restore = () => {
      navigating = false;
      visible();
    };
    const removed = new MutationObserver(() => {
      if (form.isConnected) return;
      clearRetry();
      removed.disconnect();
      document.removeEventListener('visibilitychange', visibility);
      document.removeEventListener('read-applied', visible);
      window.removeEventListener('resize', visible);
      window.removeEventListener('pagehide', navigation);
      window.removeEventListener('pageshow', restore);
      if (active === visible) active = null;
    });
    removed.observe(document.body, { childList: true, subtree: true });
    document.addEventListener('visibilitychange', visibility);
    document.addEventListener('read-applied', visible);
    // Crossing a breakpoint can uncover the conversation with the panel open.
    // Added after side-panel-v3.js's listener, so it runs after that decides.
    window.addEventListener('resize', visible);
    window.addEventListener('pagehide', navigation);
    window.addEventListener('pageshow', restore);
    active = visible;
    visible();
  });
})();
