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
    const needed = () => !navigating && document.visibilityState === 'visible' && form.isConnected;
    const clearRetry = () => {
      clearTimeout(timer);
      timer = null;
    };
    const cursor = () => attempted === null ? snapshot : stream.dataset.appliedCursor || snapshot;
    const visible = () => {
      if (!needed() || inFlight || timer !== null) return;
      if (succeeded === null || BigInt(cursor()) > succeeded) htmx.trigger(form, 'read-visible');
    };
    form.addEventListener('htmx:configRequest', event => {
      form.elements.cursor.value = cursor();
      event.detail.parameters.cursor = cursor();
    });
    form.addEventListener('htmx:beforeRequest', event => {
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
      } else if (event.detail.xhr.status === 429) {
        if (!needed()) return;
        const header = event.detail.xhr.getResponseHeader('Retry-After');
        const seconds = header === null ? NaN : Number(header);
        const after = Number.isFinite(seconds) ? seconds * 1000 : Date.parse(header) - Date.now();
        // The initial cap bounds our backoff; a server's longer minimum wins.
        const cap = Math.min(8000, 1000 * 2 ** Math.min(failures++, 3));
        const delay = Math.max(cap * (0.5 + Math.random() / 2), after || 0);
        clearRetry();
        timer = setTimeout(() => {
          timer = null;
          visible();
        }, delay);
        return;
      }
      // Flush newer shown work after failure without retrying the same cursor.
      else if (BigInt(cursor()) <= attempted) return;
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
      window.removeEventListener('pagehide', navigation);
      window.removeEventListener('pageshow', restore);
      if (active === visible) active = null;
    });
    removed.observe(document.body, { childList: true, subtree: true });
    document.addEventListener('visibilitychange', visibility);
    document.addEventListener('read-applied', visible);
    window.addEventListener('pagehide', navigation);
    window.addEventListener('pageshow', restore);
    active = visible;
    visible();
  });
})();
