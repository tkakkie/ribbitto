// Read by the build's compile shim (tools/svelte) and, later, svelte-check.
export default {
  compilerOptions: {
    runes: true,
    // Builds fragments element by element, without innerHTML (decision 19).
    fragments: "tree",
    // The version marker writes window.__svelte, a global.
    discloseVersion: false,
    css: "external",
  },
};
