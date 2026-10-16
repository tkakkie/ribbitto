# Web contracts: DOM ids and data attributes

DOM ids and script-read data attributes shared by templ, htmx and scripts.

- **DOM ids are a contract.** Templates define ids used by `hx-target`,
  `hx-select`, `hx-select-oob` and scripts; rename all references together. Contract ids
  are `organization-stream`, `sidebar-resync`, `stream-sink`, `account-menu`, `channel-create`, `conversation`, `message-list`, `message-items`, `load-older`,
  `typing-form`, `typing-status`, `unread-divider`, `message-body`, `message-composer`, `message-composer-state`, `message-help`, `message-send-alert`, `message-status`,
  `read-form`, `side-panel`, `panel-close`, `panel-backdrop`, `panel-control-<name>`, `members-list`, `branch-form`, `branch-to`, `branch-name` and `branch-feedback`, plus
  `pins-section`, `pin-form-message-<hex>` and `pin-<true|false>-message-<hex>` for pins,
  `message-<hex>` from `MessageDOMID` and `select-message-<hex>` for its
  checkbox, `channel-<hex>` from `ChannelDOMID` and `topic-<hex>` from
  `TopicDOMID` for sidebar `<li>` entries, `link-channel-<hex>` and
  `link-topic-<hex>` ids for focus restoration, `sidebar-channels`,
  `sidebar-topics` for whole lists and `topic-heading-count` for every topic heading, and `presence-<hex>` from `PresenceDOMID` for member indicators
  (32 lowercase hex digits). Load older selects `#message-items > li`.
- **Script-read data attributes are a contract too** (#351). The same renaming
  rule covers `data-event-cursor`, `data-posted-message`, `data-invalid`, `data-announcement` and:

  | Attribute | On | Present | Meaning |
  |---|---|---|---|
  | `data-pins-seq` | `#pins-section` | always | snapshot sequence |
  | `data-pinned-ids` | `#pins-section` | always | space-separated message IDs |
  | `data-pin-control` | pin form | always | message ID |
  | `data-pinned` | pin form | always | pin state |
  | `data-pin-action` | pin button | always | `true`: Unpin; `false`: Pin |
  | `data-applied-cursor` | `#organization-stream` | after durable application | newest sequence applied to the DOM, including while hidden |
  | `data-oldest-seq` | `#load-older` | always | `event_seq` of the oldest loaded message, `0` once nothing older remains; a moved item is inserted only at or above it |
  | `data-topic` | `#message-items` | topic pages only | the topic's ID, compared with a move's topics |
  | `data-from-topic`, `data-to-topic` | a `messages-moved` payload's `<ul>` | always | the move's source and destination topic IDs |
  | `data-event-seq` | each message `<li>` | always | its `event_seq`, which orders an inserted item |
  | `data-source` | each message's checkbox | always | its topic's ID; a selection stays within one source topic |
  | `data-snapshot-seq` | `#sidebar-channels` | always | organisation event sequence read in the same repeatable-read snapshot as the sidebar counts ([decision 34](../decisions/34-live-unread-counts.md)) |
  | `data-unread-count` | every sidebar badge, including the heading badge | always, including zero | numeric unread count; a zero badge remains present but hidden ([decision 34](../decisions/34-live-unread-counts.md)) |
  | `data-unconfirmed-key` | `#message-composer` | after a send could not be confirmed | that send's key, until the key changes ([composer](web-javascript.md#composer)) |
  | `data-sent-body` | `#message-composer-state` | an htmx 409 whose text starts with it | the stored body of the requester's message the key names; the help then shows no error |
  | `data-unconfirmed`, `data-not-sent`, `data-still-unconfirmed`, `data-recovered`, `data-repeat` | `#message-send-alert` | latest composers | localised alert texts |
  | `data-drafts-horizon` | the sign-in form | signed-out renders only | UUIDv7; stored drafts of sessions from earlier milliseconds are removed ([drafts](web-javascript.md#drafts)) |

  Member identity, selected topic and the resync trigger are specified in the
  [sidebar script contract](web-javascript.md#javascript).
