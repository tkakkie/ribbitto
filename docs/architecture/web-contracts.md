# Web contracts: DOM ids and data attributes

DOM ids and script-read data attributes shared by templ, htmx and scripts.

- **DOM ids are a contract.** Templates define ids used by `hx-target`,
  `hx-select`, `hx-select-oob` and scripts; rename all references together. Contract ids
  are `organization-stream`, `sidebar-resync`, `stream-sink`, `account-menu`, `channel-create`, `conversation`, `message-list`, `message-items`, `load-older`,
  `typing-form`, `typing-status`, `unread-divider`, `message-body`, `quote-form`, `composer-quote`, `quote-text`, `composer-quote-description`, `message-composer`, `message-composer-state`, `message-help`, `message-send-alert`, `message-sending`, `message-status`, `stream-stopped`,
  `message-pending`, `message-pending-template`, `message-pending-body`, `message-pending-edit-help` (template; clones append their send UUID), `message-empty`,
  `read-form`, `side-panel`, `panel-close`, `panel-backdrop`, `panel-control-<name>`, `members-list`, `branch-form`, `branch-to`, `branch-name` and `branch-feedback`, plus
  `mention-picker`, `mention-search`, `mention-status`, `mention-listbox` and `mention-option-<hex>` for mention candidates,
  `pins-section`, `pin-form-message-<hex>` and `pin-<true|false>-message-<hex>` for pins,
  `message-<hex>` from `MessageDOMID` and `select-message-<hex>` for its
  checkbox, `channel-<hex>` from `ChannelDOMID` and `topic-<hex>` from
  `TopicDOMID` for sidebar `<li>` entries, `link-channel-<hex>` and
  `link-topic-<hex>` ids for focus restoration, `sidebar-channels`,
  `sidebar-topics` for whole lists and `topic-heading-count` for every topic heading, and `presence-<hex>` from `PresenceDOMID` for member indicators
  (32 lowercase hex digits). Load older selects `#message-items > li`.
- Menu Quote also omits `type` and `hx-*`, retaining `form="quote-form"`,
  `name="quote"` and its message ID value. The existing composer glue copies
  the native submitter name/value into htmx parameters (#1500).
- **Script-read data attributes are a contract too** (#351). The same renaming
  rule covers `data-event-cursor`, `data-posted-message`, `data-invalid`, `data-announcement` and:

  | Attribute | On | Present | Meaning |
  |---|---|---|---|
  | `data-query`, `data-count` | `#mention-listbox` | always | query as received and localized result count for the picker's polite status; empty results use the no-members text |
  | `data-member-id`, `data-handle` | mention option | always | canonical member UUID and handle, in search order |
  | `data-pins-seq` | `#pins-section` | always | snapshot sequence |
  | `data-pinned-ids` | `#pins-section` | always | space-separated message IDs |
  | `data-pin-control` | pin form | always | message ID |
  | `data-pinned` | pin form | always | pin state |
  | `data-pin-action` | pin button | always | `true`: Unpin; `false`: Pin |
  | `method`, `action` | pin form | always | normal POST fallback; no `hx-*` on this form or its buttons |
  | `formaction`, `hidden` | pin button | action always; hidden on inactive action | native submit with no `type` or `hx-*`; delegated submit uses `event.submitter`, or the non-hidden pin button when absent |
  | `data-applied-cursor` | `#organization-stream` | after durable application | newest sequence applied to the DOM, including while hidden |
  | `data-oldest-seq` | `#load-older` | always | `event_seq` of the oldest loaded message, `0` once nothing older remains; a moved item is inserted only at or above it |
  | `data-topic` | `#message-items` | topic pages only | the topic's ID, compared with a move's topics |
  | `data-from-topic`, `data-to-topic` | a `messages-moved` payload's `<ul>` | always | the move's source and destination topic IDs |
  | `data-event-seq` | each message `<li>` | always | its `event_seq`, which orders an inserted item |
  | `data-quote-handle` | body member reference | always, empty if unresolved | viewer-independent handle for selection extraction |
  | `data-quote-selection` | submit button immediately after each body | always, initially `hidden` | selection-only submitter of `#quote-form`; no `type` or `hx-*`; shared across viewers |
  | `data-source` | each message's checkbox | always | its topic's ID; a selection stays within one source topic |
  | `data-snapshot-seq` | `#sidebar-channels` | always | organisation event sequence read in the same repeatable-read snapshot as the sidebar counts ([decision 34](../decisions/34-live-unread-counts.md)) |
  | `data-unread-count` | every sidebar badge, including the heading badge | always, including zero | numeric unread count; a zero badge remains present but hidden ([decision 34](../decisions/34-live-unread-counts.md)) |
  | `data-drafts-horizon` | the sign-in form | signed-out renders only | UUIDv7; stored drafts of sessions from earlier milliseconds are removed ([drafts](web-composer.md#drafts)) |
  | `data-session`, `data-session-expires`, `data-rendered-at` | `#organization-stream` | always | the session's id (a UUIDv7, never its token), its expiry and the render time in Unix milliseconds: the draft key's session, cleanup horizon and deadline for stopping writes ([drafts](web-composer.md#drafts)) |

  Member identity, selected topic and the resync trigger are specified in the
  [sidebar script contract](web-javascript-unread.md).

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
Tags preserve camelCase script payloads: exported Go fields cannot be camelCase,
and unexported fields on unused payload structs trip the `unused` lint.
Use the constant in templ, then run `go generate ./internal/web/view`
(also part of `make generate`).

The committed `web/src/contracts.gen.ts` supplies names, selectors, dynamic id
helpers and typed element, attribute and event accessors; `make check-tools`
rejects drift. Element helpers drop trailing `ID`; `DOMPrefix` becomes `Entry`,
or a leading `Link` moves to the end (`ChannelLink`). Accessors use `getName`,
selectors `nameSelector`, and dynamic ids `nameID`.
Modules still validate attribute values and external event payloads at runtime.
The lists above remain the human-readable contract.
The composer payloads are `message-sending` (`key`, `body`, `inPlace`),
`message-sent` (`key`, `message`), `message-pending-action` (`key`, nullable
`restoredKey`, `kind` restricted to `edit|remove`) and `message-draft-restored`
(`key`). All fields are strings except boolean `inPlace`, nullable `restoredKey`
and nullable `key` in `message-sending` and `message-sent`. On plain and history
pages without an idempotency key, `message-sent` already carries null
(`FormData.get`), but the unchanged `message-composer-v21.js` in-place
`message-sending` carries undefined (`elements.idempotency_key?.value`). The
contract says null; #1824 may normalize undefined to null because no consumer
distinguishes them: `message-drafts-v7.js` returns on a falsy key,
`message-composer-v21.js` does not listen to `message-sending`, and
`internal/web/drafts_save_test.go` compares only string keys. `message-sent`
must keep null because both scripts compare its key with `!==`.
The same `message-sending` string is an id (composer
markup) and an event name (script) by design; #1822's literal check must tolerate it.
Generated dispatch helpers create events with `bubbles: true`, as legacy script
dispatches do, because document-level listeners depend on it; `composed` is not
set (no script sets it and the page uses no shadow DOM).
`pending-send` and `typing-input` trigger htmx forms with no payload.
The pending edit help clone suffix includes the hyphen before its send key.
`data-state` and `data-draft-key` are script-written hooks, not server state.
Coverage already includes the composer before its typed entry conversion (#1824);
#1822 rejects stray literals in authored sources. Unconverted scripts keep their
existing strings.
