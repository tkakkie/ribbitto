-- name: CountChannelUnread :many
WITH gaps AS (
    SELECT ids.channel_id, los.lo_before, his.hi, ids.n
    FROM unnest(sqlc.arg(gap_channel_ids)::uuid[]) WITH ORDINALITY AS ids(channel_id, n)
    JOIN unnest(sqlc.arg(gap_los_before)::bigint[]) WITH ORDINALITY AS los(lo_before, n) ON los.n = ids.n
    JOIN unnest(sqlc.arg(gap_his)::bigint[]) WITH ORDINALITY AS his(hi, n) ON his.n = ids.n
), bounded AS (
    SELECT c.channel_id, messages.event_seq
    FROM unnest(sqlc.arg(channel_ids)::uuid[]) AS c(channel_id)
    CROSS JOIN LATERAL (
        SELECT m.event_seq
        FROM (SELECT lo_before, hi, n FROM gaps WHERE gaps.channel_id = c.channel_id ORDER BY n) g
        CROSS JOIN LATERAL (
            SELECT event_seq FROM message
            WHERE organization_id = sqlc.arg(organization_id)
              AND channel_id = c.channel_id
              AND event_seq > g.lo_before AND event_seq < g.hi
            ORDER BY event_seq LIMIT 100
        ) m
        ORDER BY g.n, m.event_seq LIMIT 100
    ) messages
)
SELECT c.channel_id::uuid AS channel_id,
    (SELECT count(*) FROM bounded WHERE bounded.channel_id = c.channel_id) AS unread_count,
    coalesce((SELECT event_seq FROM bounded WHERE bounded.channel_id = c.channel_id ORDER BY event_seq LIMIT 1), 0)::bigint AS first_unread
FROM unnest(sqlc.arg(channel_ids)::uuid[]) AS c(channel_id);
