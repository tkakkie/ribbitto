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
