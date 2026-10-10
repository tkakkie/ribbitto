-- name: LockChannelRead :one
SELECT channel_id FROM channel_read
WHERE organization_id = $1 AND channel_id = $2 AND member_id = $3
FOR UPDATE;

-- name: InsertChannelRead :exec
INSERT INTO channel_read (organization_id, channel_id, member_id) VALUES ($1, $2, $3);

-- name: RangePredecessor :one
SELECT lo, hi FROM read_range
WHERE organization_id = $1 AND channel_id = $2 AND member_id = $3 AND lo <= sqlc.arg(lo)
ORDER BY lo DESC LIMIT 1;

-- name: DeleteRangeNeighbours :many
DELETE FROM read_range
WHERE organization_id = $1 AND channel_id = $2 AND member_id = $3
  AND sqlc.arg(lower_lo)::bigint <= lo AND lo <= sqlc.arg(hi)::bigint
  AND sqlc.arg(lo)::bigint <= hi
RETURNING lo, hi;

-- name: InsertReadRange :exec
INSERT INTO read_range (organization_id, channel_id, member_id, lo, hi) VALUES ($1, $2, $3, $4, $5);

-- name: FirstChannelReadRanges :many
SELECT c.id::uuid AS channel_id, r.lo, r.hi
FROM unnest(sqlc.arg(channel_ids)::uuid[]) AS c(id)
CROSS JOIN LATERAL (
  SELECT lo, hi FROM read_range
  WHERE organization_id = sqlc.arg(organization_id)
    AND member_id = sqlc.arg(member_id) AND channel_id = c.id
  ORDER BY lo LIMIT 101
) AS r
ORDER BY c.id, r.lo;
