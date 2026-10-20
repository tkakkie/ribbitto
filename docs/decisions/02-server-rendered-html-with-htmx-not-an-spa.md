# 2. Server-rendered HTML with htmx, not an SPA

**Decided:** templ renders HTML on the server; htmx and a little plain
JavaScript add interactivity. No npm, no JavaScript build step.
**Why:** one language and one codebase, no API contract to keep in sync, a
single binary to deploy, less context for AI tools. Use cases return plain
structs, so a JSON API can be added later.
**Considered:** Svelte/SvelteKit — a higher ceiling for rich client-side UI,
at the cost of a second language, a build chain and an API layer. Native
apps are not planned; if they come, they will need that JSON API anyway.
**Amended by:** [decision 40](40-plain-javascript-by-default-library-islands-by-measured-exception.md) (plain JavaScript stays the default; a feature may adopt a scoped UI-library island, and npm or a build step only as an exception its own decision names).

**Amended by:** [decision 43](43-typescript-with-a-go-driven-build.md) (Go-driven build and limited Node/npm; server-rendered and no-JavaScript core retained).
