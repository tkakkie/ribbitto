# Web front-end conventions

[Decision 43](../decisions/43-typescript-with-a-go-driven-build.md);
[web toolchain](web-toolchain.md).

## Rules

- **Modules:** new and converted modules live
  in `web/src/<concern>.ts` (`web/src/*.ts`), one file per concern, using
  erasable TypeScript only (no enums, runtime namespaces or parameter
  properties) and relative imports naming `.ts`
  (`allowImportingTsExtensions`, #1410). Never author served content-hashed names. Authored JavaScript directly in `web/static/` is rejected.

- **Logic and DOM:** modules own one concern. Decisions (state transitions,
  queue order, retry classification) live in DOM-free `web/src/<concern>.ts`;
  DOM code stays thin. Test layers and `web/test/<name>.test.ts` harness rules:
  [`web-tests.md`](web-tests.md).

- **State:** a module's own working state is held in typed in-memory objects,
  never in `dataset`, limited to what
  [decision 34](../decisions/34-live-unread-counts.md),
  [decision 37](../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md)
  and [decision 41](../decisions/41-client-storage-indexeddb-by-data-class.md)
  already allow; any other/new client-side state is for #1361 to decide, not for
  this document. The attributes in [`web-contracts.md`](web-contracts.md)
  remain the contract between server HTML, CSS, scripts and tests; scripts read and write them only through
  the typed DOM-contract accessors (#1413), and a value that only the script
  needs is never put in the DOM. Render derived attributes from typed state; notify the reload guard on queue changes.

- **Script declarations:** templates use only logical names from the Go
  registry, never script files or hashed bundles.

- **Shared helpers:** extract only when duplication appears, as
  [decision 40 §2](../decisions/40-plain-javascript-by-default-library-islands-by-measured-exception.md#2-the-m7-composer)
  states. With a second module user, move it to `web/src/<helper>.ts`;
  import by relative `.ts` path and test per [`web-tests.md`](web-tests.md).

- **DOM contract:** Go owns DOM ids, dynamic suffixes, data attributes,
  event names and payload types; `go generate` writes their constants, types
  and accessors to `web/src/contracts.gen.ts`.
  A module never contains a literal contract id, attribute name, `dataset` key,
  selector or custom event string outside that adapter; runtime validation and
  type narrowing stay in the modules because static types cannot validate server
  attributes or htmx event detail. `tools/contractliterals` in `make check-tools`
  rejects literal bypasses in authored TypeScript and view templates; only the
  generated file is exempt. Computed/indirect bypasses remain for review; this
  check is not a security boundary. Removing or weakening it needs its own
  review (#1414).

## Composer projections

`web/src/composer-queue.ts` owns pending state, uncertainty, original text, confirmed ID
and immutable `draftKey` (`requestKey` may rotate after a 409).
Never read pending state, identity or original text back from DOM projections.

`data-state`, `data-unconfirmed` and item `data-posted-message` are output only;
`draftKey` stays in memory. Queue notifications publish the reload predicate
even without a DOM write or after the last removal. The stream must
subscribe before querying it; the guard owns request-class and alert clauses.
