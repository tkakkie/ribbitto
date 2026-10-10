-- name: InsertMessage :one
INSERT INTO message (organization_id, channel_id, topic_id, member_id, body, event_seq)
VALUES ($1, $2, $3, $4, $5, $6) RETURNING *;

-- name: GetMessage :one
SELECT * FROM message
WHERE organization_id = $1 AND channel_id = $2 AND event_seq = $3;

-- name: ListMessagesBefore :many
SELECT * FROM message
WHERE organization_id = sqlc.arg(organization_id) AND channel_id = sqlc.arg(channel_id)
  AND (sqlc.narg(topic_id)::uuid IS NULL OR topic_id = sqlc.narg(topic_id)::uuid)
  AND (sqlc.narg(before_event_seq)::bigint IS NULL OR event_seq < sqlc.narg(before_event_seq)::bigint)
ORDER BY event_seq DESC
LIMIT sqlc.arg('limit');

-- name: GetMessages :many
SELECT * FROM message
WHERE organization_id = $1 AND channel_id = $2 AND id = ANY(sqlc.arg(message_ids)::uuid[])
ORDER BY event_seq DESC;

-- name: FirstChannelMessageAfter :one
SELECT event_seq FROM message
WHERE organization_id = $1 AND channel_id = $2 AND event_seq > $3
ORDER BY event_seq LIMIT 1;

-- name: LastChannelMessageBefore :one
SELECT event_seq FROM message
WHERE organization_id = $1 AND channel_id = $2 AND event_seq < $3
ORDER BY event_seq DESC LIMIT 1;

-- name: TopicUnreadRangeBounds :many
SELECT coalesce((SELECT p.event_seq + 1 FROM message p
        WHERE p.organization_id = sqlc.arg(organization_id) AND p.channel_id = sqlc.arg(channel_id)
          AND p.event_seq < m.event_seq ORDER BY p.event_seq DESC LIMIT 1), 1)::bigint AS lo,
       coalesce((SELECT n.event_seq FROM message n
        WHERE n.organization_id = sqlc.arg(organization_id) AND n.channel_id = sqlc.arg(channel_id)
          AND n.event_seq > m.event_seq ORDER BY n.event_seq LIMIT 1), m.event_seq + 1)::bigint AS hi
FROM message m
WHERE m.organization_id = sqlc.arg(organization_id) AND m.channel_id = sqlc.arg(channel_id)
  AND m.topic_id = sqlc.arg(topic_id)
  AND m.event_seq > sqlc.arg(prefix_before)::bigint
  AND (m.event_seq > sqlc.arg(floor)::bigint
       OR (m.event_seq <= sqlc.arg(floor)::bigint AND m.moved_event_seq > sqlc.arg(floor)::bigint))
  AND m.event_seq <= sqlc.arg(cursor)::bigint
  AND (m.moved_event_seq IS NULL OR m.moved_event_seq <= sqlc.arg(cursor)::bigint)
  AND NOT (sqlc.arg(read_set)::int8multirange @> m.event_seq)
ORDER BY m.event_seq;

-- name: TopicChangedBetween :one
SELECT EXISTS (SELECT 1 FROM message
 WHERE organization_id = sqlc.arg(organization_id) AND channel_id = sqlc.arg(channel_id)
   AND topic_id = sqlc.arg(topic_id)
   AND ((event_seq > sqlc.arg(after_seq)::bigint AND event_seq < sqlc.arg(before_seq)::bigint)
     OR (moved_event_seq > sqlc.arg(after_seq)::bigint AND moved_event_seq < sqlc.arg(before_seq)::bigint)));
