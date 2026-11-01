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
  #1823, including typed payloads, nullable keys and htmx triggers). The legacy composer stays unchanged until
  its typed entry conversion (#1824). A
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

## Composer queue example: before and after

**Before typed queue adoption (`web/src/composer.ts`).**
Queue state (`"queued"`, `"sending"`, `"failed"`, `"confirmed"`), uncertainty
(`data-unconfirmed`) and draft identity (`data-draft-key`) still live in
DOM attributes, through generated accessors. The original logic was:

```js
  const pendingForms = () => [...document.querySelectorAll("#message-pending [data-pending-form]")];
  const dispatch = () => {
    if (pendingRequests.size || document.getElementById("message-composer")?.classList.contains("htmx-request")) return;
    const forms = pendingForms();
    if (forms.some(form => form.dataset.state === "failed" && form.dataset.unconfirmed)) return;
    const head = forms.find(form => form.dataset.state === "queued");
    if (head) htmx.trigger(head, "pending-send");
  };
```

**Queue specification now; DOM adoption planned (#1824, #1825, #1417).**
[`web/src/composer-queue.ts`](../../web/src/composer-queue.ts) holds typed state
with immutable `draftKey` separate from `requestKey`;
[`web/test/composer-queue.test.mjs`](../../web/test/composer-queue.test.mjs)
specifies transitions and ordering. The module is not served or used by the
legacy script yet. Its injected clock and key generator keep tests DOM-free:

```ts
import { createComposerQueue, holdsReload } from "./composer-queue.ts";

const queue = createComposerQueue(now, newKey, () => {
  // Called synchronously after mutation, including removal of the last item.
  notifyResetGuard(holdsReload(queue.items));
});
const item = queue.dispatch(ordinaryRequestInFlight);
if (item) sendPending(item);
```

The adapter above is illustrative. #1825 adopts the module and writes
`data-state`, `data-unconfirmed` and item `data-posted-message` as projections;
`draftKey` stays in memory. The stream guard still reads the DOM until #1417
switches its item predicate to `holdsReload` and the synchronous notification.
The composer's request and alert clauses stay with the guard.
