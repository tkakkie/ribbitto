-- name: GetMessagePin :one
SELECT pinned_by_member_id, pinned_at FROM message
WHERE organization_id = $1 AND channel_id = $2 AND id = $3;

-- name: CountChannelPins :one
SELECT count(*) FROM message
WHERE organization_id = $1 AND channel_id = $2 AND pinned_at IS NOT NULL;

-- name: SetMessagePin :execrows
UPDATE message SET pinned_by_member_id = $4, pinned_at = $5
WHERE organization_id = $1 AND channel_id = $2 AND id = $3;

-- name: ClearMessagePin :execrows
UPDATE message SET pinned_by_member_id = NULL, pinned_at = NULL
WHERE organization_id = $1 AND channel_id = $2 AND id = $3;

-- name: ListChannelPins :many
SELECT * FROM message
WHERE organization_id = $1 AND channel_id = $2 AND pinned_at IS NOT NULL
ORDER BY pinned_at DESC, id
LIMIT sqlc.arg('limit');
