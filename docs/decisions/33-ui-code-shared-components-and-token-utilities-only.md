# 33. UI code: shared components and token utilities only

**Decided** (#882, at the maintainer's request before M5's page work):
a look used in two places becomes a shared templ component, and pages
compose components instead of repeating class strings; templates style only
through token utilities, never arbitrary values or variants in square
brackets, which `tools/sourcecheck` rejects; `web/styles/app.css` holds only
tokens, base styles and what utilities cannot express. The rules are in
[`ui-code.md`](../ui-code.md) and the component list in
[`ui-components.md`](../ui-components.md); decision 2 (no
JavaScript or CSS build step beyond the pinned Tailwind CLI) is unchanged.
**Why:** M5 is built by two implementers across about ten issues; without
one place per look and a check against one-off values, buttons, fields and
spacing drift from page to page.
**Considered:** a written convention without a check (drifts silently);
component classes with `@apply` in `app.css` (a second styling system next
to the utilities); a class-sorting formatter (needs npm, and components
remove most repeated strings anyway).
