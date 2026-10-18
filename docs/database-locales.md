# Database locales

Text B-tree indexes inherit the database collation unless explicitly overridden.
With a glibc locale, an OS or base-image upgrade can change ordering and leave
indexes inconsistent. This includes account email, organisation slug, member
handle, channel name and the topic expression index `topic_name_idx` in
`db/migrations/00008_topic.sql`.

## New clusters

For PostgreSQL 17+, initialise new ribbitto clusters with the builtin provider:

```sh
initdb -D /path/to/new/data --encoding=UTF8 \
  --locale-provider=builtin --builtin-locale=C.UTF-8 --locale=C.UTF-8
```

The matching locale also sets the remaining locale categories explicitly.
See the [initdb options](https://www.postgresql.org/docs/17/app-initdb.html).
Builtin uses PostgreSQL's own operations rather than glibc collation data;
other locale services, such as messages, can still use the OS.

Builtin `C.UTF-8` sorts by Unicode code point and uses Unicode simple case
mapping. It does not provide language-specific sorting or full case folding.
See [locale providers](https://www.postgresql.org/docs/17/locale.html#LOCALE-PROVIDERS)
and [collations](https://www.postgresql.org/docs/18/collation.html#COLLATION-MANAGING).
Review PostgreSQL major-version upgrade notes too: independence from glibc
does not promise unchanged character semantics across PostgreSQL versions.

The channel uniqueness constraint in `db/migrations/00005_channel_message.sql`
is on `(organization_id, name)`, so differently cased names remain distinct.
The topic index instead compares `lower(name)` within each channel, excluding
NULL names. Lowercasing follows the input expression's collation and character
classification provider; code-point ordering alone does not determine it.
With the recommended database default, non-ASCII letters use builtin simple
case mapping. Before moving data, inspect the installed index definitions
with `\d channel` and `\d topic` in psql (the channel rule may change), and
compare representative names on both servers:

```sql
SELECT name, lower(name) FROM (VALUES ('É'), ('é'), ('İ'), ('ß')) AS names(name);
SELECT 'É'::text = 'é'::text AS same_channel_name,
       lower('É'::text) = lower('é'::text) AS same_topic_name;
```

A libc database cannot switch providers in place. To adopt builtin, dump and
restore into a new cluster initialised as above; check name collisions under
the target rules before cutover. Follow the
[restore procedure](../README.md#restoring-a-backup), including stopping ribbitto.
This does not change column collations by migration.

## Development volumes

`compose.yml` retains the image's libc default to keep development aligned
with the existing CI and headless-review setups, and to exercise libc
deployments. For builtin development, use this explicit override. The image
accepts `POSTGRES_INITDB_ARGS` only when initialising an empty data directory;
changing it does not convert an existing volume.

To discard an existing development cluster, first stop its users and back up
anything needed. `make db-down` preserves the volume; the command below deletes
**all databases in that Compose volume**, including other worktrees' databases.
Use the same Compose project selection as when starting it.

```sh
docker compose down --volumes
docker compose -f compose.yml -f - up -d --wait <<'YAML'
services:
  postgres:
    environment:
      POSTGRES_INITDB_ARGS: --encoding=UTF8 --locale-provider=builtin --builtin-locale=C.UTF-8 --locale=C.UTF-8
YAML
```

For [per-worktree databases](worktree-env.md), `make ai-env-clean` drops only
that worktree's database and releases its reservation; it does not reinitialise
the server or change its templates' locale. After recreating the cluster, run
`make ai-env` in each worktree using that server's admin configuration to
recreate/migrate its database. New databases normally inherit the template's
locale. Verify the provider on the actual server; worktree databases may use
a different server from Compose.

## Upgrades of libc or ICU clusters

Before an OS, image or collation-library upgrade, take a backup and plan a
maintenance window. After a glibc change, check every database, including
worktree databases, before allowing application traffic. PostgreSQL may report
`WARNING: database "ribbitto" has a collation version mismatch` on connection.
Inspect recorded and actual default-collation versions:

```sql
SELECT datname, datlocprovider, datcollate, datctype, datcollversion,
       pg_database_collation_actual_version(oid) AS actual_version
FROM pg_database
ORDER BY datname;
```

For libc (`c`) and ICU (`i`), differing non-NULL versions indicate a mismatch.
The [actual-version function](https://www.postgresql.org/docs/18/functions-admin.html)
compares against the installed provider; the
[database catalog](https://www.postgresql.org/docs/18/catalog-pg-database.html)
describes the recorded fields. Builtin (`b`) has no external library version;
do not interpret its version fields as a glibc version. NULL means there is no
version available to compare, not proof that indexes are healthy. Glibc's
version is only a proxy: distributions can backport collation changes without
changing it.

In each database, also check explicitly named collations:

```sql
SELECT oid::regcollation, collversion,
       pg_collation_actual_version(oid) AS actual_version
FROM pg_collation
WHERE collprovider IN ('c', 'i')
  AND collversion <> pg_collation_actual_version(oid);
```

The [ALTER COLLATION notes](https://www.postgresql.org/docs/18/sql-altercollation.html#SQL-ALTERCOLLATION-NOTES)
include a dependency query for named collations. Default-collation indexes
also need attention; that dependency query alone is not a complete inventory.

Keep ribbitto stopped while repairing a changed collation. Connect to the
affected database as an authorised maintenance role and rebuild affected
indexes, including unique and expression indexes. A conservative option for
ribbitto's user indexes is the following (replace the database name):

```sql
REINDEX DATABASE ribbitto;
ALTER DATABASE ribbitto REFRESH COLLATION VERSION;
```

Run these as separate statements outside a transaction block. `REINDEX DATABASE`
rebuilds user indexes in the connected database, excluding system catalogs,
and blocks writes while each index is rebuilt. For a planned individual rebuild,
`REINDEX INDEX CONCURRENTLY public.topic_name_idx;` reduces write blocking,
but takes more work and has failure/cleanup caveats; it does not make an
already inconsistent index safe to use. See
[REINDEX](https://www.postgresql.org/docs/18/sql-reindex.html).

A corrupted unique index can have admitted duplicates; rebuilding may fail
with a uniqueness violation. Resolve conflicting rows under the new rules and
retry successfully before resuming traffic. Follow the REINDEX manual's cleanup
instructions if a concurrent attempt leaves an invalid index.

Only after all affected objects are rebuilt, refresh the database version;
for affected named collations use `ALTER COLLATION ... REFRESH VERSION` too.
[REFRESH COLLATION VERSION](https://www.postgresql.org/docs/18/sql-alterdatabase.html)
only updates metadata: it neither rebuilds indexes nor verifies the repair.
Recheck versions and successful index rebuilds before restarting ribbitto.
