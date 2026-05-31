-- Facts has a regular table + a vec_facts vec0 companion. Per-row vector
-- INSERT/DELETE and the cosine-distance RecallFacts query stay raw because
-- sqlc cannot parse the vec0 virtual table syntax.

-- name: InsertFact :execlastid
INSERT INTO facts(session_id, user_id, text, kind, source_msg_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: UpdateFactText :execrows
UPDATE facts SET text = ?, updated_at = ?
WHERE id = ? AND deleted_at IS NULL;

-- name: SoftDeleteFact :exec
UPDATE facts SET deleted_at = ?
WHERE id = ? AND deleted_at IS NULL;

-- name: ListFacts :many
SELECT id, session_id, COALESCE(user_id, '') AS user_id, text, kind,
       COALESCE(source_msg_id, 0) AS source_msg_id, created_at, updated_at
FROM facts
WHERE session_id = ? AND deleted_at IS NULL
ORDER BY updated_at DESC
LIMIT ?;
