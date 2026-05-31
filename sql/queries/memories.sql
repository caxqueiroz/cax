-- The memories table holds the regular columns (id, session_id, text,
-- source_msg_id, created_at). The companion vec_memories virtual table holds
-- the embedding column and is queried via raw SQL because sqlc cannot parse
-- vec0 syntax. AddMemory in Go does both INSERTs in one transaction.
--
-- Recall (cosine-distance search against vec_memories) stays raw.

-- name: InsertMemory :execlastid
INSERT INTO memories(session_id, text, source_msg_id, created_at)
VALUES (?, ?, ?, ?);

-- name: CountMemories :one
SELECT COUNT(*) FROM memories;
