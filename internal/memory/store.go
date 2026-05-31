package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	_ "modernc.org/sqlite"     // registers the "sqlite" database/sql driver
	_ "modernc.org/sqlite/vec" // registers the vec0 virtual table + vec_* functions

	"github.com/caxqueiroz/cax/internal/config"
	"github.com/caxqueiroz/cax/internal/memory/memorydb"
)

// Role is a message author role.
type Role string

const (
	// RoleUser is a message authored by the user.
	RoleUser Role = "user"
	// RoleAssistant is a message authored by the assistant.
	RoleAssistant Role = "assistant"
	// RoleSystem is a system/instruction message.
	RoleSystem Role = "system"
	// RoleTool is tool output fed back into the conversation.
	RoleTool Role = "tool"
)

// Message is a single stored conversation message.
type Message struct {
	ID        int64
	SessionID string
	Role      Role
	Content   string
	Tokens    int
	CreatedAt time.Time
}

// Stats is a snapshot of memory storage size.
type Stats struct {
	DBSizeBytes  int64
	MessageCount int
	MemoryCount  int
}

// Store is the SQLite-backed memory store. queries is the sqlc-generated
// type-safe surface for every non-vec0 operation; vec_memories / vec_facts
// inserts and cosine-distance lookups stay on the raw *sql.DB because sqlc
// can't parse the vec0 virtual-table syntax.
type Store struct {
	db       *sql.DB
	queries  *memorydb.Queries
	embedder Embedder
	dim      int
	dbPath   string
}

// Open opens (creating if needed) the SQLite DB, runs the schema migration with
// N = embedder.Dim(), and records/verifies the embedding model+dim in meta.
func Open(cfg config.MemoryConfig, embedder Embedder) (*Store, error) {
	if embedder == nil {
		return nil, errors.New("memory: embedder is required")
	}
	dim := embedder.Dim()
	if dim <= 0 {
		return nil, fmt.Errorf("memory: embedder dim must be positive, got %d", dim)
	}
	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", cfg.DBPath, err)
	}
	// One connection keeps :memory: DBs coherent and avoids vec0 cross-conn issues.
	db.SetMaxOpenConns(1)

	s := &Store{db: db, queries: memorydb.New(db), embedder: embedder, dim: dim, dbPath: cfg.DBPath}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.verifyMeta(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS messages (
		  id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, role TEXT NOT NULL,
		  content TEXT NOT NULL, token_count INTEGER NOT NULL, created_at TIMESTAMP NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS summaries (
		  id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, summary_text TEXT NOT NULL,
		  covers_up_to_msg_id INTEGER NOT NULL, created_at TIMESTAMP NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS memories (
		  id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, text TEXT NOT NULL,
		  source_msg_id INTEGER, created_at TIMESTAMP NOT NULL)`,
		fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS vec_memories USING vec0(memory_id INTEGER PRIMARY KEY, embedding float[%d])`, s.dim),
		`CREATE TABLE IF NOT EXISTS usage (
		  id INTEGER PRIMARY KEY AUTOINCREMENT, ts TIMESTAMP NOT NULL, provider TEXT, model TEXT,
		  input_tokens INTEGER NOT NULL, output_tokens INTEGER NOT NULL, kind TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS schedules (
		  id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT UNIQUE NOT NULL, cron_expr TEXT NOT NULL,
		  prompt TEXT NOT NULL, channel TEXT NOT NULL, enabled INTEGER NOT NULL, last_run TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT)`,
		// facts: mem0-style extracted atoms. Populated by the FactExtractor
		// LLM when memory.mode != snippets. deleted_at is a soft-delete marker
		// so the extractor can supersede contradicted facts without losing
		// audit trail.
		`CREATE TABLE IF NOT EXISTS facts (
		  id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, user_id TEXT,
		  text TEXT NOT NULL, kind TEXT NOT NULL DEFAULT '',
		  source_msg_id INTEGER, created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL,
		  deleted_at TIMESTAMP)`,
		`CREATE INDEX IF NOT EXISTS idx_facts_session_live ON facts(session_id) WHERE deleted_at IS NULL`,
		fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS vec_facts USING vec0(fact_id INTEGER PRIMARY KEY, embedding float[%d])`, s.dim),
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

// verifyMeta writes embed model+dim on first run, or fails fast if the stored dim
// differs from the configured embedder's dim.
func (s *Store) verifyMeta() error {
	ctx := context.Background()
	storedDim, err := s.queries.GetMeta(ctx, "embed_dim")
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if err := s.queries.SetMeta(ctx, memorydb.SetMetaParams{
			Key:   "embed_model",
			Value: sql.NullString{String: s.embedder.Model(), Valid: true},
		}); err != nil {
			return fmt.Errorf("write meta model: %w", err)
		}
		if err := s.queries.SetMeta(ctx, memorydb.SetMetaParams{
			Key:   "embed_dim",
			Value: sql.NullString{String: strconv.Itoa(s.dim), Valid: true},
		}); err != nil {
			return fmt.Errorf("write meta dim: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("read meta: %w", err)
	}
	if !storedDim.Valid {
		return fmt.Errorf("memory: stored embed_dim is NULL")
	}
	n, convErr := strconv.Atoi(storedDim.String)
	if convErr != nil {
		return fmt.Errorf("parse stored embed_dim %q: %w", storedDim.String, convErr)
	}
	if n != s.dim {
		return fmt.Errorf("memory: embed dim mismatch: stored %d, configured %d (re-embed migration required)", n, s.dim)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close db: %w", err)
	}
	return nil
}

// Stats returns DB file size plus message and memory counts.
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	mc, err := s.queries.CountMessages(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("count messages: %w", err)
	}
	st.MessageCount = int(mc)
	memc, err := s.queries.CountMemories(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("count memories: %w", err)
	}
	st.MemoryCount = int(memc)
	if info, err := os.Stat(s.dbPath); err == nil {
		st.DBSizeBytes = info.Size()
	} // :memory: and unstat-able paths -> 0, not an error
	return st, nil
}
