# Svelte islands (draft)

Draft from #1166, placed on #1152's never-merged prototype branch so the
prototype is written by it. It merges, with its checks, only through #1166.

## Sources

Write Svelte from the official documentation, not from memory: much of what
AI tools learned is Svelte 4. The Svelte MCP server (`@sveltejs/mcp`, its
`list-sections`, `get-documentation` and `svelte-autofixer`) or
`https://svelte.dev/docs/svelte/llms.txt` are recommended. The checks in
#1166 enforce the rules below; where the autofixer and the pinned
`svelte-check` disagree, the pinned check wins.

## Rules

- **Svelte 5 runes only.** `$state`, `$derived`, `$effect`, `$props()`,
  `onclick=`-style event attributes and snippets. No `export let`, `$:`,
  `on:click`, `<slot>`, `createEventDispatcher`, `beforeUpdate`,
  `afterUpdate` or `svelte/store` in components.
- **Same rules as templ.**
  - No HTML from strings: no `{@html}`, no `innerHTML`/`outerHTML`
    assignments, `insertAdjacentHTML`, `document.write`,
    `createContextualFragment`, `DOMParser` or `setHTMLUnsafe`.
  - Token utilities and shared looks only ([decision 33](../decisions/33-ui-code-shared-components-and-token-utilities-only.md)).
    No `<style>` block, `style=` attribute or `style:` directive: the CSP's
    `style-src 'self'` blocks injected styles.
  - UI strings come from `internal/web/i18n/locales/{en,ja}.toml` through
    server-rendered markup or attributes, never a second catalogue.
  - Follow [`docs/accessibility.md`](../accessibility.md).
  - Keyboard handling is IME-aware: `isComposing`, keyCode 229, and Enter
    within 100 ms of `compositionend`.
- **Islands hold client state only.** Never the message list, sidebar,
  counts or presence. Server data arrives as server-rendered attributes or
  through htmx. Holding, requests and storage go no further than
  [`web-javascript.md`](web-javascript.md) and
  [decision 37](../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md)
  allow: no `fetch`, `XMLHttpRequest`, `navigator.sendBeacon`, `WebSocket`,
  `EventSource` or `htmx.ajax`; no `eval`, `new Function` or string timers;
  no IndexedDB; no writes to `window.*` or `globalThis.*`.
- **Mounting** (prototype): a mount script on scoped `htmx:load` that
  replaces the server-rendered control with the island's own, keeping what
  was typed, and calls `unmount()` on `htmx:beforeCleanupElement`. #1152's
  results settle the production choice.
- **Files.** Sources in `web/islands/src/`; one minified IIFE per entry in
  `web/static/islands/`, versioned `-vN` like other immutable assets, with no
  chunks or dynamic imports. Components are tested by the go-rod
  `TestBrowser` suite.
- **Toolchain upgrades** come in their own pull request, after reading the
  official changelog.

## What only review and browser tests can check

What is stored and whether it is a permitted setting; that an island owns no
application state beyond decision 37; that every UI string comes from the
server; IME-aware keyboard handling; accessibility beyond the compiler's
warnings; computed class names and indirect access that pattern rules
cannot resolve. The compiled output in `web/static/islands/` is the pinned
compiler's generated DOM construction and is not checked as authored code.
