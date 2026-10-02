# 22. Replies stay in the stream, with a reply-chain panel

**Decided:** a reply is an ordinary message with a nullable
`reply_to_message_id`, shown in time order with a one-line preview of the
message it answers (*planned*). Selecting the preview or the reply opens
the **reply chain** in a side panel, or a full-screen sheet on narrow
screens: the path from the first message to the selected one and every
reply below the selected one, as one list in `event_seq` order. Like
channel history, the chain opens at the selected message with a bounded
page and links to earlier and later parts; without JavaScript it is an
ordinary page (17, 19). Exact limits are left to implementation. There is
no separate place to post or unread count; opening the chain leaves the
read position unchanged. Rules: [`docs/domain/replies.md`](../domain/replies.md).
**Why:** answers in a busy topic need context without leaving the stream
or competing with topics as a place to talk (#275).
**Considered:** Slack-style threads (answers leave the stream and need
their own unread model); quoting the answered message's text (a copy that
diverges on edit or delete).
