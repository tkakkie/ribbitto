// Visibility and DOM application are UX glue; htmx owns every request.
(() => {
  const initialized = new WeakSet();
  document.addEventListener('htmx:load', event => {
    const root = event.detail.elt;
    const form = root.id === 'read-form' ? root : root.querySelector('#read-form');
    if (!form || initialized.has(form)) return;
    initialized.add(form);
    const stream = document.getElementById('organization-stream');
    const snapshot = form.elements.cursor.value;
    let sent = null;
    let inFlight = false;
    const cursor = () => sent === null ? snapshot : stream.dataset.appliedCursor || snapshot;
    const visible = () => {
      if (document.visibilityState !== 'visible' || !form.isConnected || inFlight) return;
      if (sent === null || BigInt(cursor()) > sent) htmx.trigger(form, 'read-visible');
    };
    form.addEventListener('htmx:configRequest', event => {
      form.elements.cursor.value = cursor();
      event.detail.parameters.cursor = cursor();
    });
    form.addEventListener('htmx:beforeRequest', event => {
      if (document.visibilityState !== 'visible' || inFlight) {
        event.preventDefault();
        return;
      }
      inFlight = true;
      sent = BigInt(cursor());
    });
    form.addEventListener('htmx:afterRequest', () => {
      inFlight = false;
      // htmx releases its request lock after afterRequest returns. Read the
      // newest cursor then, rather than queueing snapshots of older triggers.
      queueMicrotask(visible);
    });
    document.addEventListener('visibilitychange', visible);
    document.addEventListener('read-applied', visible);
    visible();
  });
})();
