-- name: TopicReadRanges :many
SELECT lo, hi FROM read_range
WHERE organization_id = $1 AND channel_id = $2 AND member_id = $3
ORDER BY lo;

-- name: TopicReadFloor :one
SELECT floor_seq FROM topic_read_floor
WHERE organization_id = $1 AND channel_id = $2 AND member_id = $3 AND topic_id = $4;

-- name: BatchRangeNeighbours :many
-- Requires the prefix row (lo = 0) that Prepare creates under the channel lock:
-- without a predecessor the subquery is NULL and the range matches nothing.
SELECT DISTINCT r.lo, r.hi FROM
unnest(sqlc.arg(los)::bigint[]) WITH ORDINALITY AS a(lo, n)
JOIN unnest(sqlc.arg(his)::bigint[]) WITH ORDINALITY AS b(hi, n) ON a.n = b.n
JOIN read_range r ON r.lo <= b.hi
WHERE r.organization_id = sqlc.arg(organization_id)
  AND r.channel_id = sqlc.arg(channel_id) AND r.member_id = sqlc.arg(member_id)
  AND a.lo <= r.hi
  AND (SELECT p.lo FROM read_range p
       WHERE p.organization_id = sqlc.arg(organization_id)
         AND p.channel_id = sqlc.arg(channel_id) AND p.member_id = sqlc.arg(member_id)
         AND p.lo <= a.lo ORDER BY p.lo DESC LIMIT 1) <= r.lo;

-- name: DeleteBatchRanges :exec
DELETE FROM read_range
WHERE organization_id = $1 AND channel_id = $2 AND member_id = $3
  AND lo IN (SELECT value FROM unnest(sqlc.arg(los)::bigint[]) AS a(value));

-- name: InsertBatchRanges :exec
WITH bounds AS (
  SELECT a.lo, b.hi FROM
  unnest(sqlc.arg(los)::bigint[]) WITH ORDINALITY AS a(lo, n)
  JOIN unnest(sqlc.arg(his)::bigint[]) WITH ORDINALITY AS b(hi, n) ON a.n = b.n
)
INSERT INTO read_range (organization_id, channel_id, member_id, lo, hi)
SELECT sqlc.arg(organization_id), sqlc.arg(channel_id), sqlc.arg(member_id), lo, hi FROM bounds;

-- name: InsertTopicReadFloor :exec
INSERT INTO topic_read_floor (organization_id, channel_id, member_id, topic_id, floor_seq)
VALUES ($1, $2, $3, $4, $5);

-- name: RaiseTopicReadFloor :exec
UPDATE topic_read_floor SET floor_seq = sqlc.arg(floor_seq)
WHERE organization_id = $1 AND channel_id = $2 AND member_id = $3 AND topic_id = $4
  AND floor_seq < sqlc.arg(floor_seq);

-- name: ReadTopicState :many
-- Ranges are separate from floors, so each range is returned only once.
WITH requested AS (
  SELECT t.id, t.n
  FROM unnest(sqlc.arg(topic_ids)::uuid[]) WITH ORDINALITY AS t(id, n)
)
SELECT 0::bigint AS topic_n, r.lo AS value, r.hi
FROM read_range r
WHERE r.organization_id = sqlc.arg(organization_id)
  AND r.channel_id = sqlc.arg(channel_id) AND r.member_id = sqlc.arg(member_id)
UNION ALL
SELECT t.n, f.floor_seq, 0::bigint
FROM requested t
JOIN topic_read_floor f ON true
WHERE f.organization_id = sqlc.arg(organization_id)
  AND f.channel_id = sqlc.arg(channel_id) AND f.member_id = sqlc.arg(member_id)
  AND f.topic_id = t.id
ORDER BY topic_n, value;
