package store

// Reduce retained messages to one metadata row per typed conversation before
// reusing the CTE. SQLite otherwise repeats visibility/deletion probes for the
// three aggregates, or materializes all 25k message rows. The covering index
// supplies peer order so the grouping does not need to sort message content.
const panelStatusCountsQuery = `WITH visible AS MATERIALIZED (
 SELECT m.peer_type,m.peer_id,count(*) AS messages,max(m.first_seen_at) AS last_seen
 FROM messages m WHERE ` + localVisibleMessage + ` GROUP BY m.peer_type,m.peer_id)
 SELECT coalesce(sum(messages),0),
 (SELECT count(*) FROM (SELECT peer_type,peer_id FROM dialogs WHERE account=? UNION SELECT peer_type,peer_id FROM visible)),
 max(last_seen) FROM visible`
