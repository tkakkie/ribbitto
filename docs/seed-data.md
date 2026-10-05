# Development seed data

`cmd/seed` requires an empty, disposable development database. Before
connecting, it refuses `RIBBITTO_DATABASE_URL` hosts (including fallbacks)
other than `localhost`, `127.0.0.1` or `::1`. Loopback can still reach a
local production database or a remote tunnel; use only a disposable one.
With `RIBBITTO_DATABASE_URL` exported:

```sh
go run ./cmd/ribbitto migrate up
go run ./cmd/seed -messages 20000
```

`-messages N` is the positive number of scripted messages **per channel**
before topic fixtures (default 100). The command creates Paper Lantern
Studio (`paper-lantern`), five fictional members and four channels including
`general`. Each run prints a fresh random password shared by its members:
sign in as `mira@example.test` (owner), or another script handle at
`example.test`. Nothing fixed or published signs in.

Development mode then adds `rooftop-garden` and `garden-time` in `general`.
`topic.Brancher.Branch` moves one extra script message into each, leaving
notices and durable `messages.moved` events for replay.
`conversation.Posting.PostToTopic` adds `conversation.PageSize + 10` (60) more posts
to `rooftop-garden`: 64 extra messages including notices, also with
`-messages 1`. The original N posts remain in each default topic.

After seeding, run `make dev`, sign in at `http://localhost:8080/` and open
`general` to see the notices and topic labels. Select `rooftop-garden`
from the sidebar to see **Load older** (61 messages; 11 on the older page),
or `garden-time` for one branched message. Topic URLs are
`/organizations/paper-lantern/channels/<channelID>/topics/<topicID>`;
IDs vary per run.

For load tests on a disposable machine, add `-streams 300
-streams-per-account 16 -sessions-per-account 2 -output /tmp/loadtest.json`
(on one command line). The cap defaults to 16, matching the production per-account stream cap (#160);
this flag only sizes fixtures and changes no production caps or defaults.
The command creates `max(5, ceil(streams / cap))` accounts in the same
organisation, including the five fictional members: 300 / 16 needs 19,
so it adds 14. Sessions per account default to 1; multiple sessions still
share that account's stream cap. Without load-test flags, no sessions or
credential file are created. Load-test mode adds no named topics or topic
fixtures; each channel still gets its default topic. All runs limit total
messages (including development fixtures and notices) plus accounts ×
sessions to 100,000, checked without overflow before writes. With four
channels, development mode therefore accepts at most `-messages 24984`.

The named JSON file is created exclusively with mode `0600`; existing files
and paths inside Git repositories (including worktrees) are refused before
database writes. Its format is
`{"organization_slug":"paper-lantern","channel_ids":["<UUID>"],"accounts":[{"handle":"mira","tokens":["<token>"]}]}`.
All channels and accounts are included. Tokens are fresh sessions with normal
30-day expiry; only their hashes reach PostgreSQL. Plain tokens go only to
this file, never to stdout or logs. Keep it outside repositories.

Loopback alone does not prove a database is disposable. These credentials
work against **any server using the seeded database**: keeping the database
and every server using it on the disposable machine is an operating
requirement. After a run (successful or interrupted), stop those servers,
drop the disposable database and delete the credential file, which may be
empty or incomplete on failure. Start again with a fresh migrated database;
deleting the file alone does not invalidate sessions.

`cmd/seed/conversations.json` contains English and Japanese exchanges,
Unicode names and layout edge cases. Have a person review scripts before
committing. Exchanges repeat in file order until N posts, possibly ending
partway through. Identical flags reproduce members, channels, bodies,
authors and order; IDs, timestamps and hashes vary. Existing use cases
post messages now, without backdating.

Completed setup makes the command refuse all writes, including on rerun.
Each use case commits separately: after an interrupted seed, discard the
disposable database and start with a fresh migrated one. Seeding enables
sign-up only inside this command; it does not change the server's settings.
