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

// Store is the SQLite-backed memory store.
type Store struct {
	db       *sql.DB
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

	s := &Store{db: db, embedder: embedder, dim: dim, dbPath: cfg.DBPath}
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
	var storedDim string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key='embed_dim'`).Scan(&storedDim)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := s.db.Exec(`INSERT INTO meta(key, value) VALUES ('embed_model', ?), ('embed_dim', ?)`,
			s.embedder.Model(), strconv.Itoa(s.dim)); err != nil {
			return fmt.Errorf("write meta: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("read meta: %w", err)
	}
	n, convErr := strconv.Atoi(storedDim)
	if convErr != nil {
		return fmt.Errorf("parse stored embed_dim %q: %w", storedDim, convErr)
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
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&st.MessageCount); err != nil {
		return Stats{}, fmt.Errorf("count messages: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memories`).Scan(&st.MemoryCount); err != nil {
		return Stats{}, fmt.Errorf("count memories: %w", err)
	}
	if info, err := os.Stat(s.dbPath); err == nil {
		st.DBSizeBytes = info.Size()
	} // :memory: and unstat-able paths -> 0, not an error
	return st, nil
}
