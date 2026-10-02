# 2. Server-rendered HTML with htmx, not an SPA

**Decided:** templ renders HTML on the server; htmx and a little plain
JavaScript add interactivity. No npm, no JavaScript build step.
**Why:** one language and one codebase, no API contract to keep in sync, a
single binary to deploy, less context for AI tools. Use cases return plain
structs, so a JSON API can be added later.
**Considered:** Svelte/SvelteKit — a higher ceiling for rich client-side UI,
at the cost of a second language, a build chain and an API layer. Native
apps are not planned; if they come, they will need that JSON API anyway.
