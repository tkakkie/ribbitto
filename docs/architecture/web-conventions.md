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
  names, with constants, types and typed accessors (#1413; composer #1823). A
  module never contains a literal contract id, attribute name, `dataset` key,
  selector or custom event string outside that adapter; runtime validation and
  type narrowing stay in the modules because static types cannot validate server
  attributes or htmx event detail, and a Go check with negative fixtures rejects
  literal bypasses in authored sources (#1822, #1414).

## Composer queue example: before and after

**Before (`web/static/message-composer-v21.js`, lines 69–76 and 518–535).**
Queue state (`"queued"`, `"sending"`, `"failed"`, `"confirmed"`), uncertainty
(`dataset.unconfirmed`) and draft identity (`dataset.draftKey`, line 146) live
in `dataset` alongside raw DOM selectors:

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

**After (illustrative sketch until #1416 and #1417 land; not committed code).**
DOM-free queue logic (`web/src/<composer-queue>.ts`) holds typed state in memory
with immutable `draftKey` separate from `requestKey`; the thin DOM adapter
(`web/src/<composer>.ts`) renders `data-state` as an output from the typed queue
state via the generated accessor (#1413, #1417) and notifies the stream reset
guard on state transitions. The sketch leaves out message selection and the
400/404/413/422/429 alert handling that lines 518–535 of
`web/static/message-composer-v21.js` also do:

```ts
// Illustration (#1416/#1417): pure logic in web/src/<composer-queue>.ts, no DOM.
export type PendingState = "queued" | "sending" | "failed" | "confirmed";
export interface PendingItem {
  readonly draftKey: string;
  requestKey: string;
  state: PendingState;
  unconfirmed: boolean;
}
export function nextToDispatch(items: readonly PendingItem[], busy: boolean): PendingItem | undefined {
  if (busy || items.some((i) => i.state === "failed" && i.unconfirmed)) return undefined;
  return items.find((i) => i.state === "queued");
}
export function settlePending(item: PendingItem, status: number, newKey: () => string): void {
  if (status === 200) {
    item.state = "confirmed";
    item.unconfirmed = false;
    return;
  }
  item.state = "failed";
  if (status === 409) {
    item.unconfirmed = false;
    item.requestKey = newKey();
  } else if (![400, 404, 413, 422, 429].includes(status)) {
    item.unconfirmed = true;
  }
}

// Illustration (#1416/#1417): thin DOM adapter in web/src/<composer>.ts.
import { setIdempotencyKey, setPendingState } from "./contracts.gen.ts";

function onPendingResponse(item: PendingItem, form: HTMLFormElement, status: number, notifyResetGuard: () => void): void {
  settlePending(item, status, () => crypto.randomUUID());
  setIdempotencyKey(form, item.requestKey);
  setPendingState(form, item.state); // data-state rendered from typed state (#1417)
  notifyResetGuard(); // notifies stream reset guard even without DOM mutation (#1416/#1417)
}
```
