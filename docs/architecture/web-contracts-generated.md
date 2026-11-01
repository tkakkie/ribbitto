# Web contracts: generated contract

Generated DOM-contract constants, TypeScript accessors and literal checks for [`web-contracts.md`](web-contracts.md).

## Generated contract

`internal/web/view/contracts.go` owns the sidebar and composer ids, dynamic id
prefixes, attribute names, custom events and htmx triggers. The composer set
includes the draft channel/topic hooks and the stream cursor/topic names it reads;
other scripts' private hooks remain outside the generated contract.

Composer attributes in the generated set:

| Attribute | On | Present | Meaning |
|---|---|---|---|
| `data-unconfirmed-key` | `#message-composer` | after a send could not be confirmed | that send's key, until the key changes ([composer](web-composer.md#composer)) |
| `data-posted-topic`, `data-posted-seq` | `#message-composer-state` | with `data-posted-message` | posted message's topic ID and posting sequence, including repeats; read once for topic pending removal |
| `data-send-key` | `#message-composer-state` | every render | account mode: `enter` or `modifier_enter`; read each keydown and load/state swap for `enterkeyhint` |
| `data-sent-body` | `#message-composer-state` | an htmx 409 with a verified typed prefix, unquoted | only the request text's prefix verified by the keyed message's typed-body proof; the help then shows no error |
| `data-unconfirmed`, `data-not-sent`, `data-still-unconfirmed`, `data-recovered`, `data-repeat`, `data-busy`, `data-capacity` | `#message-send-alert` | latest composers | localised alert texts |
| `data-pending-body`, `data-pending-form`, `data-pending-actions`, `data-pending-edit`, `data-pending-edit-help`, `data-pending-remove`, `data-pending-sending` | pending template/clone | latest feeds/topics | form and state/control hooks; form `data-state` tracks queued/sending/failed/confirmed; `data-unconfirmed` on the form remembers uncertainty, `data-posted-message` on the item confirms its ID |
| `data-draft-channel`, `data-draft-topic` | `#message-composer` | latest htmx feeds/topics only; the latter only on topics; neither on `?before=`/`?around=` | canonical channel/topic UUIDs for the draft key's place; stay outside `#message-composer-state` and survive 200/422 swaps ([drafts](web-composer.md#drafts)) |

Add a documented constant with a `//dom:id`
or `//dom:suffix` directive and its DOM element type, `//dom:attribute`, or
`//dom:event` and its Go payload struct name. Directives belong to a constant's
own doc/line comment, never a const group. Payloads support string, bool, numbers
and nullable string (`*string`). A string field's `dom:"edit,remove"` tag restricts
it to those literal choices. Fields use the `json:"name"` tag before any comma, otherwise the Go
name; empty names and `-` also fall back. Duplicate resulting names fail.
Tags preserve camelCase: exported Go fields cannot be camelCase; unexported
payload fields on unused structs trip the `unused` lint.
Use the constant in templ, then run `go generate ./internal/web/view`
(also part of `make generate`).

The committed `web/src/contracts.gen.ts` supplies names, selectors, dynamic id
helpers and typed element, attribute and event accessors; `make check-tools`
rejects drift. Element helpers drop trailing `ID`; `DOMPrefix` becomes `Entry`,
or a leading `Link` moves to the end (`ChannelLink`). Accessors use `getName`,
selectors `nameSelector`, and dynamic ids `nameID`.
Modules still validate attribute values and external event payloads at runtime.
The human-readable DOM contract (ids and data attributes) is described in [`web-contracts.md`](web-contracts.md); this page covers the generated one.

The composer payloads are `message-sending` (`key`, `body`, `inPlace`),
`message-sent` (`key`, `message`), `message-pending-action` (`key`, nullable
`restoredKey`, `kind` restricted to `edit|remove`) and `message-draft-restored`
(`key`). Fields are strings except boolean `inPlace` and nullable `restoredKey`
and sending/sent `key`. Without an idempotency key on plain/history pages,
`message-sent` carries null (`FormData.get`); `web/src/composer.ts` normalizes
in-place `message-sending` from undefined (`elements.idempotency_key?.value`)
to null, as the contract specifies, because consumers treat
both alike: `message-drafts-v7.js` returns on a falsy key, the composer does not
listen to `message-sending`, and `internal/web/drafts_save_test.go` compares only
string keys. `message-sent` must keep null: both scripts compare its key with `!==`.
`message-sending` is both an id (composer markup) and an event name by design;
the literal check accepts contract constants in either role.
Generated dispatches use `bubbles: true`, like legacy scripts, for document
listeners; `composed` stays unset (no script sets it; no shadow DOM).
`pending-send` and `typing-input` trigger htmx forms with no payload.
The pending edit help clone suffix includes the hyphen before its send key.
`data-state` and `data-draft-key` are script-written hooks, not server state.
Sidebar and composer hooks are covered, ahead of composer conversion (#1824).

`tools/contractliterals`, run by `make check-tools`, rejects literal ids,
attribute names, dataset keys, selectors and custom events in authored
TypeScript under `web/src/`; only `web/src/contracts.gen.ts` is exempt.
It also checks literal ids, attribute names and contract hooks in `hx-target`,
`hx-select`, `hx-select-oob`, `hx-sync` and `hx-trigger` in view templates.
Dynamic id literals match the prefix itself or a complete 32-lowercase-hex
suffix, avoiding unrelated ids such as `channel-create`.
This is a text check, not a security boundary: computed/indirect uses such as
`getElementById(prefix + key)`, aliasing and names built at runtime remain
for review. Unconverted scripts in `web/static/` keep their existing strings
and are not scanned. Removing or weakening this check needs its own review.
