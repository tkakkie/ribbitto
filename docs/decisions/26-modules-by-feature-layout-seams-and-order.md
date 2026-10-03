# 26. Modules by feature: layout, seams and order

**Decided** (#362, completing [decision 14](14-a-modular-monolith-by-feature-migrated-after-m3.md)):

- **Layout.** Each module has three kinds of package:
  - `internal/<module>`, its API: types, errors, use cases, and the consumer
    interfaces it needs;
  - `internal/<module>/internal/postgres`, its store and generated queries,
    which may import the root;
  - a wiring package `internal/<module>/<module>pg`, the only importer of
    the store. It exports `New(pool)`, which returns the module's use
    cases, and `Tx`- or `Snapshot`-taking factories for the operations that
    other modules' flows need. The composition roots call it.

  The modules are `identity` (accounts, passwords, sessions, sign-in), `org`
  (organisations, members, authorisation, setup and sign-up), `channel`,
  `topic`, `message` and `realtime`. Sign-up moves from `identity` to `org`:
  it adds a member to the installation's organisation. With it, setup and
  sign-up share their validation without an `identity` → `org` edge, which
  resolves #154 M11.
- **Kernel.** `internal/kernel` is not a home for shared things that are
  hard to place. It holds only IDs, stable value types, and cross-module
  contracts with no natural single owner. Today that is `ID`.
  `Membership`, `Organization` and the authorisation entry point belong to
  `org`; the other modules import `org`'s root. Nothing moves there because
  several modules use it.
- **Platform.** `internal/platform/postgres` holds the pool, the migration
  runner, database-lifecycle test helpers, and transaction and snapshot
  *capabilities*: opening, committing and rolling back a `Tx`, and opening
  a read-only repeatable-read `Snapshot`. Module APIs and use cases see
  only these opaque handles. A separate bridge package unwraps them to pgx,
  and depguard lets only stores under `**/internal/postgres/**` import it.
  So pgx types and SQL stay inside each module. It shares capabilities, not
  query helpers.
- **Cross-module operations bound to a `Tx` or `Snapshot`**, writes and
  reads alike, use consumer-owned interfaces, whatever the import
  direction. The orchestrating use case owns the transaction or snapshot
  and declares what it needs as a factory that takes the handle. The
  owning module's wiring package implements it, and the composition root
  injects it. A lower module's root is imported for its types and its use
  cases only. Preserved:
  - organisation-first locking;
  - `event_seq` order, including branching's move before its notice;
  - rollback of every write and increment;
  - the retention boundary advancing with its deletion;
  - `Raise` only after the outer commit.
- **Realtime** owns `event_log`, retention, the hub, the stream loop, cursors
  and `reset`, and an envelope: organisation, sequence, kind,
  `AudienceMemberID`, channel, routing topics, and an opaque payload. Each
  publishing module owns its kinds' payload schema, encoding and decoding,
  registered at wiring. HTML renderers stay in `internal/web` as per-kind
  adapters. Preserved:
  - one snapshot for a batch's events and the organisation's sequence
    bounds. The bounds are `org`'s columns, read through an injected
    `Snapshot`-bound reader;
  - the legacy rendered-topic fallback for `message.posted` rows without a
    topic;
  - posting-time routing;
  - a malformed known payload failing its batch;
  - unknown kinds advancing the cursor;
  - authorisation after rendering, immediately before sending.
- **Web.** `internal/web` stays one UI shell. The stream endpoint gets its
  scope check from a resolver that `channel` and `topic` provide.
- **sqlc and migrations.** There is one `sql` entry per module in
  `sqlc.yaml`, each with `db/queries/<module>/` and its own output.
  `db/migrations` stays one goose directory, owned by `platform/postgres`.
  The page snapshot (#154 M7) becomes a use case that passes one
  `Snapshot` to each module's reader.
- **Enforcement.** Go `internal` directories, plus per-module depguard:
  - a module imports only `kernel`, `platform` and lower modules' roots;
  - wiring packages are imported only by `cmd/*` and tests, apart from a
    temporary exception that names its removal step.
- **Graph** (each module may import those after its arrow; the graph is
  acyclic):
  - `realtime` and `identity` → kernel, platform;
  - `org` → `identity`, `realtime`;
  - `channel` → `org`;
  - `topic` → `org`, `realtime`;
  - `message` → `identity`, `org`, `channel`, `topic`, `realtime`;
  - `web` → every module root;
  - `cmd/*` → roots and wiring packages.

  Some injected operations go against the import direction, and injection
  keeps them acyclic:
  - setup's default channel;
  - a channel's default topic;
  - branching's moves and notice;
  - retention's boundary;
  - realtime's sequence bounds.
- **Order.**
  0. kernel and platform;
  1. `identity` (the pilot);
  2. `realtime`;
  3. `org`;
  4. `channel`;
  5. `topic`;
  6. `message`;
  7. removal of the remaining layers and temporary exceptions.

  Until its flows move, `internal/infra/postgres` gets each moved store as
  an injected `Tx`- or `Snapshot`-taking factory, never by importing a
  wiring package. The flows are posting, branching, setup, sign-up and the
  page snapshot. `features.md` lists each temporary path with the step that
  introduces it and the step that removes it. Each step's issues, about 400
  lines a pull request, are written from `main` just before the step
  starts (#362). Feature work pauses meanwhile.

**Why:** the compiler, not only lint, then keeps a module's store package
private; table ownership stays a separate, reviewed rule. In addition, the shared files that caused M1's conflicts (#106) split by
module. Consumer interfaces keep one transaction per flow without cycles.
A small kernel and capability-only platform stop two new shared layers
growing in place of the old ones. `realtime` moves second, not last as
first proposed: it sits low in the graph, so moving it after its publishers
would need temporary adapters from each moved publisher to the old event log.

**Considered:** keeping the layers and enclosing features with depguard
only (no compiler guarantee); an orchestration module above the others (one
module depending on all); an outbox between modules (gives up the single
transaction that decision 5 and branching rely on); per-module web packages
(the channel page composes five modules; revisit if conflicts recur);
`Membership` in the kernel (it has a natural owner, `org`).
