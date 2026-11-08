# Web front-end conventions

[Decision 43](../decisions/43-typescript-with-a-go-driven-build.md);
[web toolchain](web-toolchain.md).

## Rules

- **Modules:** new and converted modules live
  in `web/src/<concern>.ts` (`web/src/*.ts`), one file per concern, using
  erasable TypeScript only (no enums, runtime namespaces or parameter
  properties) and relative imports naming `.ts`
  (`allowImportingTsExtensions`). Never author served content-hashed names or JavaScript in `web/static/`.

- **Logic and DOM:** modules own one concern. Decisions live in DOM-free
  `web/src/<concern>.ts`;
  DOM code stays thin. Test rules:
  [`web-tests.md`](web-tests.md).

- **Page lifecycle:** new modules use `web/src/lifecycle.ts` per module instance:
  synchronous start, freeze on persisted pagehide, thaw
  on persisted pageshow and dispose on ordinary pagehide or early removal.
  Every pageshow also reports persistence, including the initial ordinary one.
  Dispose runs before the instance's signal aborts; use that signal for listeners
  and cleanup. Modules retain their own state across freeze/thaw.

- **State:** a module's own working state is held in typed in-memory objects,
  never in `dataset`, limited to what
  [decision 34](../decisions/34-live-unread-counts.md),
  [decision 37](../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md)
  and [decision 41](../decisions/41-client-storage-indexeddb-by-data-class.md)
  already allow; new client-side state needs #1361. The attributes in [`web-contracts.md`](web-contracts.md)
  are read and written only through typed DOM-contract accessors; script-only
  values never go in the DOM. Render derived attributes from typed state; notify the reload guard on queue changes.

- **Script declarations:** templates use only logical names from the Go
  registry, never script files or hashed bundles.

- **Shared helpers:** extract only when duplication appears, as
  [decision 40 §2](../decisions/40-plain-javascript-by-default-library-islands-by-measured-exception.md#2-the-m7-composer)
  states. With a second module user, move it to `web/src/<helper>.ts`;
  test per [`web-tests.md`](web-tests.md).

- **DOM contract:** Go owns DOM ids, dynamic suffixes, data attributes,
  event names and payload types; `go generate` writes their constants, types
  and accessors to `web/src/contracts.gen.ts`.
  A module never contains a literal contract id, attribute name, `dataset` key,
  selector or custom event string outside that adapter; runtime validation and
  type narrowing stay in modules. `tools/contractliterals` in `make check-tools`
  rejects literal bypasses outside generated code. Review computed/indirect
  bypasses; this is no security boundary. Weakening it needs review (#1414).

## Composer projections

`web/src/composer-queue.ts` owns pending state, uncertainty, original text, confirmed ID
and immutable `draftKey` (`requestKey` may rotate after a 409).
Never read pending state, identity or original text back from DOM projections.

`data-state`, `data-unconfirmed` and item `data-posted-message` are output only;
`draftKey` stays in memory. Queue notifications publish the reload predicate
even without a DOM write or after the last removal. The stream must
subscribe before querying it; the guard owns request-class and alert clauses.
