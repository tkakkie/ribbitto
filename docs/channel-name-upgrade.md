# Channel name uniqueness upgrade

Migration 00020 replaces `channel_organization_id_name_key` with the unique
index `channel_name_idx` on `(organization_id, lower(name))` (#1289).
Dropping the constraint takes an ACCESS EXCLUSIVE lock on `channel`, held until
the transaction commits. Reads and writes of `channel` block while the unique
index builds. If case-only duplicates already exist within an organisation,
`CREATE UNIQUE INDEX` itself fails `migrate up` with a unique violation (23505)
whose detail names the duplicate key.
The transaction rolls back, including the constraint drop; no channel is
deleted or renamed automatically. Before retrying, the operator must rename
channels in each conflicting group until every name is unique ignoring case.
Find groups with:

```sql
SELECT organization_id, lower(name), array_agg(name ORDER BY name)
FROM channel
GROUP BY organization_id, lower(name)
HAVING count(*) > 1;
```

Apply each chosen replacement by both `organization_id` and `id`. Repeat the
query until no conflicting groups remain, then retry `migrate up`. Down
restores the case-sensitive constraint under its original name and drops the
new index, preserving channel data.
