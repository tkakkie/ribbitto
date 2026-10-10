// Visibility is UX glue; htmx owns the request and the form owns its snapshot.
(() => {
  document.addEventListener('htmx:load', event => {
    const root = event.detail.elt;
    const form = root.id === 'read-form' ? root : root.querySelector('#read-form');
    if (!form) return;
    const visible = () => {
      if (document.visibilityState !== 'visible' || !form.isConnected) return;
      document.removeEventListener('visibilitychange', visible);
      htmx.trigger(form, 'read-visible');
    };
    document.addEventListener('visibilitychange', visible);
    visible();
  });
})();
