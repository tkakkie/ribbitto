# Web contracts: DOM ids and data attributes

- **DOM ids are a contract.** Templates define ids used by `hx-target`,
  `hx-select`, `hx-select-oob` and scripts; rename all references together. Contract ids
  are `organization-stream`, `sidebar-resync`, `stream-sink`, `account-menu`, `channel-create`, `conversation`, `message-list`, `message-items`, `load-older`,
  `typing-form`, `typing-status`, `unread-divider`, `message-body`, `quote-form`, `composer-quote`, `quote-text`, `composer-quote-description`, `message-composer`, `message-composer-state`, `message-help`, `message-send-alert`, `message-sending`, `message-status`, `stream-stopped`,
  `message-pending`, `message-pending-template`, `message-pending-body`, `message-pending-edit-help` (template; clones append their send UUID), `message-empty`,
  `read-form`, `side-panel`, `panel-close`, `panel-backdrop`, `panel-control-<name>`, `members-list`, `branch-form`, `branch-to`, `branch-name` and `branch-feedback`, plus
  `mention-picker`, `mention-search`, `mention-status`, `mention-listbox` and `mention-option-<hex>` for mention candidates,
  `message-actions`, `message-actions-template`, `message-actions-trigger`, `resolution-form`, `resolution-summary`,
  `pins-section`, `pin-form-message-<hex>` and `pin-<true|false>-message-<hex>` for pins,
  `message-<hex>` from `MessageDOMID` and `select-message-<hex>` for its
  checkbox, `channel-<hex>` from `ChannelDOMID` and `topic-<hex>` from
  `TopicDOMID` for sidebar `<li>` entries, `link-channel-<hex>` and
  `link-topic-<hex>` ids for focus restoration, `sidebar-channels`,
  `sidebar-topics` for whole lists and `topic-heading-count` for every topic heading, and `presence-<hex>` from `PresenceDOMID` for member indicators
  (32 lowercase hex digits). Load older selects `#message-items > li`.
- Message links carry `data-message-action` (UUID), `data-message-url`,
  `data-pin-url`, `data-unpin-url` and `data-pinned`. The menu copies server-provided URLs and identity.
  Only topic pages render the Resolve menu form and `#message-items[data-resolve-url]`
  (empty for the default). Feed topic links open the topic page. Pin form IDs stay per message. Forms clone on first open; inactive forms stay
  in hidden message containers so delayed responses retain their targets and SSE state.
- Menu Quote keeps `form="quote-form"`, `name="quote"` and the message ID.
- **Script-read data attributes are a contract too** (#351). The same renaming
  rule covers `data-event-cursor`, `data-posted-message`, `data-invalid`, `data-announcement` and:

  | Attribute | On | Present | Meaning |
  |---|---|---|---|
  | `data-state` | pending form | each queued item | output-only queue projection: queued/sending/failed/confirmed; CSS/tests observe it, scripts never read it |
  | `data-unconfirmed` | pending form | uncertain outcome only | output-only queue projection; scripts never read it |
  | `data-posted-message` | pending item | response supplies an ID | output-only confirmed identity; reconciliation reads the typed item |
  | `data-query`, `data-count` | `#mention-listbox` | always | query as received and localized result count for the picker's polite status; empty results use the no-members text |
  | `data-member-id`, `data-handle` | mention option, body member reference | always (resolved on body) | canonical member UUID and handle, in search order |
  | `data-own-mention` | body `bdi[data-member-id]` | when it equals `#organization-stream`'s `data-member` | set on the viewer's own mention |
  | `data-copy-link` | `a[data-copy-link]` in the shared actions menu | always | copies its browser-resolved href |
  | `data-copied` | that anchor (`a[data-copy-link]`) | after a successful copy until the menu closes | set while the actions menu remains open |
  | `data-copied-label` | its confirmation span | always | the text announced in `#message-status` |
  | `data-pins-seq` | `#pins-section` | always | snapshot sequence |
  | `data-pinned-ids` | `#pins-section` | always | space-separated message IDs |
  | `data-pin-control` | pin form | always | message ID |
  | `data-pinned` | pin form | always | pin state |
  | `data-pin-action` | pin button | always | `true`: Unpin; `false`: Pin |
  | `method`, `action` | pin form | always | normal POST fallback; no `hx-*` on this form or its buttons |
  | `formaction`, `hidden` | pin button | action always; hidden on inactive action | native submit with no `type` or `hx-*`; delegated submit uses `event.submitter`, or the non-hidden pin button when absent |
  | `data-applied-cursor` | `#organization-stream` | after durable application | newest sequence applied to the DOM, including while hidden |
  | `data-announcement` | live message `<li>` | new-post delivery only | server-rendered text appended to the polite live region, which retains ten additions |
  | `data-oldest-seq` | `#load-older` | always | `event_seq` of the oldest loaded message, `0` once nothing older remains; a moved item is inserted only at or above it |
  | `data-topic` | `#message-items` | topic pages only | the topic's ID, compared with a move's topics |
  | `data-from-topic`, `data-to-topic` | a `messages-moved` payload's `<ul>` | always | the move's source and destination topic IDs |
  | `data-event-seq` | each message `<li>` and unread divider | always | its `event_seq`, which orders an inserted item |
  | `data-quote-handle` | body member reference | always, empty if unresolved | viewer-independent handle for selection extraction |
  | `data-quote-selection` | submit button immediately after each body | always, initially `hidden` | selection-only submitter of `#quote-form`; no `type` or `hx-*`; shared across viewers |
  | `data-source` | each message's checkbox | always | its topic's ID; a selection stays within one source topic |
  | `data-snapshot-seq` | `#sidebar-channels` | always | organisation event sequence read in the same repeatable-read snapshot as the sidebar counts ([decision 34](../decisions/34-live-unread-counts.md)) |
  | `data-unread-count` | every sidebar badge, including the heading badge | always, including zero | numeric unread count; a zero badge remains present but hidden ([decision 34](../decisions/34-live-unread-counts.md)) |
  | `data-drafts-horizon` | the sign-in form | signed-out renders only | UUIDv7; stored drafts of sessions from earlier milliseconds are removed ([drafts](web-composer.md#drafts)) |
  | `data-session`, `data-session-expires`, `data-rendered-at` | `#organization-stream` | always | the session's id (a UUIDv7, never its token), its expiry and the render time in Unix milliseconds: the draft key's session, cleanup horizon and deadline for stopping writes ([drafts](web-composer.md#drafts)) |

  Member identity, selected topic and the resync trigger are specified in the
  [sidebar script contract](web-javascript-unread.md).

The composer dispatches `message-pending-changed` on `document` synchronously
on each queue notification, with `{holdsReload: boolean}` from `holdsReload(items)`.
The stream retains that predicate and queues a microtask recheck, ignoring malformed
payloads. After attaching its
listener it dispatches `message-pending-query` (empty detail); the composer replies
synchronously with the current predicate. The composer also publishes on startup,
so either script order and changes before listener attachment work. Pending DOM
mutations never feed the reset guard; its request-class and alert inputs remain.

See the [generated contract](web-contracts-generated.md#generated-contract).
