# Web front-end conventions (planned)

Conventions for browser modules under
[decision 43](../decisions/43-typescript-with-a-go-driven-build.md) and the
[web toolchain](web-toolchain.md). They apply as scripts convert (#1416–#1421)
and to new client code (#1414); unconverted scripts keep their current files and
checks until migrated.

## Rules

- **Where modules live and how they are named:** new and converted modules live
  in `web/src/<concern>.ts` (`web/src/*.ts`), one file per concern, using
  erasable TypeScript only (no enums, runtime namespaces or parameter
  properties) and relative imports naming `.ts`
  (`allowImportingTsExtensions`, #1410). After #1411, the Go builder (esbuild's
  Go API) compiles page entries into uncommitted
  `web/static/dist/[name]-[hash].js` bundles selected through the manifest and
  Go registry; authors never write the served content-hashed name. The
  `<concern>-v<N>` pattern (`web/static/<concern>-v<N>.js`, required by
  `^.+-v[0-9]+\.js$` in `web/static/javascript_test.go`) applies only to legacy
  JavaScript in `web/static/`.

- **One concern per module; pure logic apart from DOM code:** each module owns
  one concern, and logic that decides something (state transitions, queue
  order or retry classification) lives in a DOM-free `web/src/<concern>.ts`
  module. Only that DOM-free `.ts` logic is unit-tested through the `.mjs`
  harnesses directly in `web/test/<name>.test.mjs` that
  [decision 43](../decisions/43-typescript-with-a-go-driven-build.md) sets and
  `make test-web` runs (Node's test runner, `node --test`, without `@types/node`),
  separately from strict, no-emit type checking (#1410). DOM code stays thin and
  is covered by the go-rod browser tests
  ([`web-layers-checks.md`](web-layers-checks.md)).

- **State:** a module's own working state is held in typed in-memory objects,
  never in `dataset`, limited to what
  [decision 34](../decisions/34-live-unread-counts.md),
  [decision 37](../decisions/37-composer-javascript-pending-posts-keys-and-drafts.md)
  and [decision 41](../decisions/41-client-storage-indexeddb-by-data-class.md)
  already allow; any other/new client-side state is for #1361 to decide, not for
  this document. The attributes that [`web-contracts.md`](web-contracts.md)
  documents (for example `data-event-cursor`, `data-applied-cursor`,
  `data-unread-count`, `data-state`) stay in the DOM as the contract between
  server HTML, CSS, scripts and tests; scripts read and write them only through
  the typed DOM-contract accessors (#1413), and a value that only the script
  needs is never put in the DOM. Derived attributes such as `data-state` are
  rendered from the typed state (#1416, #1417), and the composer queue notifies
  the stream reload guard when its state changes.

- **How a page declares its scripts:** a page declares its scripts in one Go
  registry (#1411), and `templ` templates reference only logical script names
  from that registry; a template never names a script file or hashed bundle. The
  registry resolves logical entries through the manifest to embedded
  `web/static/dist/[name]-[hash].js` bundles (or serves rebuilt disk bundles
  with `no-store` in development), while unconverted scripts keep their `-vN`
  tags until replaced.

- **When a shared helper is extracted:** a shared helper is extracted only once
  duplication appears, as
  [decision 40 §2](../decisions/40-plain-javascript-by-default-library-islands-by-measured-exception.md#2-the-m7-composer)
  states. A helper with a second user in another module moves to its own
  `web/src/<helper>.ts` module, imported by relative `.ts` path, and its
  DOM-free `.ts` logic is unit-tested through direct `web/test/*.test.mjs` harnesses that
  [decision 43](../decisions/43-typescript-with-a-go-driven-build.md) sets and
  `make test-web` runs.

- **How DOM ids and data attributes come from the DOM contract:** Go definitions
  used by `templ` own DOM ids (including dynamic id suffixes), data attributes,
  custom event names and payload fields/types; a Go generator under `tools/`,
  reached by `go generate`, writes the committed generated file
  `web/src/contracts.gen.ts` that the [toolchain page](web-toolchain.md)
  names, with constants, types and typed accessors (sidebar #1413 and composer
  #1823, including typed payloads, nullable keys and htmx triggers).
  `web/src/composer.ts` is the typed composer entry and uses these accessors. A
  module never contains a literal contract id, attribute name, `dataset` key,
  selector or custom event string outside that adapter; runtime validation and
  type narrowing stay in the modules because static types cannot validate server
  attributes or htmx event detail. `tools/contractliterals` in `make check-tools`
  rejects literal bypasses in authored TypeScript and view templates; only the
  generated file is exempt. It matches whole names/tokens and the current
  dynamic id families' 32-hex suffixes, not arbitrary substrings. This text
  check is not a security boundary; computed/indirect bypasses (concatenation,
  aliasing, runtime names) remain for review. Legacy `web/static/` scripts
  are outside its scope. Removing or weakening it needs its own review (#1414).

## Composer queue example

[`web/src/composer-queue.ts`](../../web/src/composer-queue.ts) owns state,
uncertainty, original text, confirmed ID and immutable `draftKey`; `requestKey`
may rotate after a 409. Its [tests](../../web/test/composer-queue.test.mjs)
specify transitions and ordering. The entry keeps item/form references in maps,
clones the server template and writes DOM projections from these typed objects.
It never reads pending state, identity or original text back from those outputs.

The entry's synchronous subscription renders each changed projection, then calls:

```ts
const notifyPending = () => dom.dispatchMessagePendingChangedEvent(document, {
  holdsReload: holdsReload(queue.items),
});
dom.listenMessagePendingQueryEvent(document, notifyPending);
notifyPending();
```

`data-state`, `data-unconfirmed` and item `data-posted-message` are output only;
`draftKey` stays in memory. Every queue notification publishes the predicate,
including a change with no DOM write and removal of the last item. The stream
subscribes before requesting the current predicate, covering either script order.
Its request-class and alert clauses stay with the guard.
