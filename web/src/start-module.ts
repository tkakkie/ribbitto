/** Preserve separate scripts' failure boundaries so later starts and lifecycle attachment survive. */
export function startModule(start: () => void, report: (error: unknown) => void = error => {
  if (typeof reportError === "function") reportError(error);
  else setTimeout(() => { throw error; });
}) {
  try {
    start();
  } catch (error) {
    report(error);
  }
}
