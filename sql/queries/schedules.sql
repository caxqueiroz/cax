-- name: ListSchedules :many
SELECT name, cron_expr, prompt, channel, enabled
FROM schedules
ORDER BY name;

-- UpsertSchedule mirrors UpsertSchedule's ON CONFLICT semantics: replaces
-- cron_expr, prompt, channel, enabled when name matches; otherwise inserts.
-- name: UpsertSchedule :exec
INSERT INTO schedules(name, cron_expr, prompt, channel, enabled)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET
    cron_expr = excluded.cron_expr,
    prompt    = excluded.prompt,
    channel   = excluded.channel,
    enabled   = excluded.enabled;

-- name: MarkScheduleRun :execrows
UPDATE schedules SET last_run = ? WHERE name = ?;
