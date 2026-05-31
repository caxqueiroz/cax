-- name: AppendMessage :execlastid
INSERT INTO messages(session_id, role, content, token_count, created_at)
VALUES (?, ?, ?, ?, ?);

-- MessagesAfter is used by MaybeSummarize: every message past the
-- covers_up_to bound, newest-first so the caller can split by token budget.
-- name: MessagesAfter :many
SELECT id, session_id, role, content, token_count, created_at
FROM messages
WHERE session_id = ? AND id > ?
ORDER BY id DESC;

-- MessagesForSession is used by LoadWindow: ALL messages for a session,
-- newest-first. The caller token-budget-trims and reverses.
-- name: MessagesForSession :many
SELECT id, session_id, role, content, token_count, created_at
FROM messages
WHERE session_id = ?
ORDER BY id DESC;

-- name: CountMessages :one
SELECT COUNT(*) FROM messages;
