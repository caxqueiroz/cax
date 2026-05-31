-- LatestSummaryText is used by LoadWindow: just the text of the most
-- recent summary for the session. Returns sql.ErrNoRows when none.
-- name: LatestSummaryText :one
SELECT summary_text FROM summaries
WHERE session_id = ?
ORDER BY id DESC
LIMIT 1;

-- LatestSummary is used by MaybeSummarize: needs both the covers_up_to
-- bound and the prior summary text to chain new content into.
-- name: LatestSummary :one
SELECT covers_up_to_msg_id, summary_text FROM summaries
WHERE session_id = ?
ORDER BY id DESC
LIMIT 1;

-- name: InsertSummary :exec
INSERT INTO summaries(session_id, summary_text, covers_up_to_msg_id, created_at)
VALUES (?, ?, ?, ?);
