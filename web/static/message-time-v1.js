(() => {
  const localize = () => {
    document.querySelectorAll("time[data-local-time]").forEach((element) => {
      const date = new Date(element.dateTime);
      if (!Number.isNaN(date.getTime())) {
        element.textContent = date.toLocaleString(document.documentElement.lang);
      }
    });
  };
  localize();
  document.addEventListener("htmx:load", localize);
})();
