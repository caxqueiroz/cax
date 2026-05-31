-- name: RecordUsage :exec
INSERT INTO usage(ts, provider, model, input_tokens, output_tokens, kind)
VALUES (?, ?, ?, ?, ?, ?);

-- UsageRollup computes 1d/1w/1m token totals. The window bound is passed as
-- a UTC timestamp; COALESCE keeps the result non-null on empty windows; the
-- CAST AS INTEGER nails the typed return so sqlc doesn't emit interface{}.
-- name: UsageRollup :one
SELECT
    CAST(COALESCE(SUM(input_tokens), 0) AS INTEGER)  AS input_tokens,
    CAST(COALESCE(SUM(output_tokens), 0) AS INTEGER) AS output_tokens
FROM usage
WHERE ts >= ?;
