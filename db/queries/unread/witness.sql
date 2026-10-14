-- name: WitnessRanges :many
SELECT channel_id, lo, hi FROM read_range
WHERE organization_id = sqlc.arg(organization_id) AND member_id = sqlc.arg(member_id)
  AND channel_id IN (SELECT id FROM unnest(sqlc.arg(channel_ids)::uuid[]) AS c(id))
ORDER BY channel_id, lo;

-- name: WitnessFloors :many
SELECT topic_id, floor_seq FROM topic_read_floor
WHERE organization_id = sqlc.arg(organization_id) AND member_id = sqlc.arg(member_id)
  AND channel_id = sqlc.arg(channel_id)
  AND topic_id IN (SELECT id FROM unnest(sqlc.arg(topic_ids)::uuid[]) AS t(id))
ORDER BY topic_id;
