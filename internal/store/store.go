// Package store records periodic snapshots of TeamSpeak server activity to a
// local SQLite database for later analysis (e.g. a "year in recap").
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	_ "modernc.org/sqlite"

	"github.com/samcm/ts-discord-status/internal/teamspeak"
)

const (
	// sampleBucket is the resolution snapshots are aligned to. Aligning each
	// snapshot to a whole minute lets re-records (e.g. after a restart) upsert
	// onto the same row instead of duplicating it.
	sampleBucket = 60

	// retentionInterval is how often expired rows are pruned.
	retentionInterval = 24 * time.Hour
)

// schema is the on-disk layout. presence is WITHOUT ROWID and references users
// by integer id so the high-cardinality table stays as small as possible.
const schema = `
CREATE TABLE IF NOT EXISTS samples (
	ts          INTEGER PRIMARY KEY,
	total_users INTEGER NOT NULL,
	max_clients INTEGER NOT NULL,
	uptime_s    INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS users (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	nickname   TEXT NOT NULL UNIQUE,
	first_seen INTEGER NOT NULL,
	last_seen  INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS presence (
	ts      INTEGER NOT NULL,
	user_id INTEGER NOT NULL,
	PRIMARY KEY (ts, user_id)
) WITHOUT ROWID;`

// pragmas are applied once on open. auto_vacuum must run before any table is
// created to take effect on a fresh database.
var pragmas = []string{
	"PRAGMA auto_vacuum=INCREMENTAL",
	"PRAGMA journal_mode=WAL",
	"PRAGMA synchronous=NORMAL",
	"PRAGMA busy_timeout=5000",
	"PRAGMA temp_store=MEMORY",
}

// Config holds recorder settings.
type Config struct {
	Path          string
	RetentionDays int
}

// Service records TeamSpeak state snapshots.
type Service interface {
	Start(ctx context.Context) error
	Stop() error
	Record(ctx context.Context, state *teamspeak.State) error
}

type service struct {
	log logrus.FieldLogger
	cfg Config
	db  *sql.DB

	mu      sync.Mutex
	userIDs map[string]int64
	done    chan struct{}
	wg      sync.WaitGroup
}

// NewService creates a new status recorder.
func NewService(log logrus.FieldLogger, cfg Config) Service {
	return &service{
		log:     log.WithField("component", "store"),
		cfg:     cfg,
		userIDs: make(map[string]int64, 32),
		done:    make(chan struct{}),
	}
}

// Start opens the database, applies the schema, and begins retention pruning.
func (s *service) Start(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.cfg.Path), 0o755); err != nil {
		return fmt.Errorf("failed to create database directory: %w", err)
	}

	db, err := sql.Open("sqlite", "file:"+s.cfg.Path)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	// A single connection serialises the once-a-minute writes and keeps the
	// applied PRAGMAs consistent, avoiding "database is locked" under WAL.
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("failed to reach database: %w", err)
	}

	for _, p := range pragmas {
		if _, err := db.ExecContext(ctx, p); err != nil {
			return fmt.Errorf("failed to apply %q: %w", p, err)
		}
	}

	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("failed to apply schema: %w", err)
	}

	s.db = db

	s.wg.Add(1)

	go s.retentionLoop()

	s.log.WithFields(logrus.Fields{
		"path":           s.cfg.Path,
		"retention_days": s.cfg.RetentionDays,
	}).Info("Status recorder started")

	return nil
}

// Stop checkpoints the WAL and closes the database.
func (s *service) Stop() error {
	close(s.done)
	s.wg.Wait()

	if s.db == nil {
		return nil
	}

	if _, err := s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		s.log.WithError(err).Warn("Failed to checkpoint database on shutdown")
	}

	return s.db.Close()
}

// Record writes a single minute-aligned snapshot of the server state.
func (s *service) Record(ctx context.Context, state *teamspeak.State) error {
	return s.recordAt(ctx, time.Now().Unix(), state)
}

// recordAt writes a snapshot aligned to the minute containing now.
func (s *service) recordAt(ctx context.Context, now int64, state *teamspeak.State) error {
	if state == nil {
		return nil
	}

	ts := now - now%sampleBucket

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO samples (ts, total_users, max_clients, uptime_s) VALUES (?, ?, ?, ?)
		 ON CONFLICT(ts) DO UPDATE SET
		     total_users = excluded.total_users,
		     max_clients = excluded.max_clients,
		     uptime_s    = excluded.uptime_s`,
		ts, state.TotalUsers, state.MaxClients, int64(state.Uptime.Seconds()),
	); err != nil {
		return fmt.Errorf("failed to record sample: %w", err)
	}

	seen := make(map[string]struct{}, state.TotalUsers)

	for _, ch := range state.Channels {
		for _, u := range ch.Users {
			nick := strings.TrimSpace(u.Nickname)
			if nick == "" {
				continue
			}

			if _, ok := seen[nick]; ok {
				continue
			}

			seen[nick] = struct{}{}

			id, err := s.userID(ctx, tx, nick, ts)
			if err != nil {
				return err
			}

			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO presence (ts, user_id) VALUES (?, ?)`, ts, id,
			); err != nil {
				return fmt.Errorf("failed to record presence: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit snapshot: %w", err)
	}

	return nil
}

// userID upserts a user's directory entry (refreshing last_seen) and returns its
// integer id, caching the lookup to avoid a SELECT on every snapshot.
func (s *service) userID(ctx context.Context, tx *sql.Tx, nick string, now int64) (int64, error) {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO users (nickname, first_seen, last_seen) VALUES (?, ?, ?)
		 ON CONFLICT(nickname) DO UPDATE SET last_seen = excluded.last_seen`,
		nick, now, now,
	); err != nil {
		return 0, fmt.Errorf("failed to upsert user: %w", err)
	}

	s.mu.Lock()
	id, ok := s.userIDs[nick]
	s.mu.Unlock()

	if ok {
		return id, nil
	}

	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE nickname = ?`, nick).Scan(&id); err != nil {
		return 0, fmt.Errorf("failed to look up user id: %w", err)
	}

	s.mu.Lock()
	s.userIDs[nick] = id
	s.mu.Unlock()

	return id, nil
}

// retentionLoop prunes rows older than the retention window once a day.
func (s *service) retentionLoop() {
	defer s.wg.Done()

	ticker := time.NewTicker(retentionInterval)
	defer ticker.Stop()

	s.prune()

	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.prune()
		}
	}
}

// prune deletes expired rows and reclaims the freed pages.
func (s *service) prune() {
	if s.cfg.RetentionDays <= 0 {
		return
	}

	cutoff := time.Now().AddDate(0, 0, -s.cfg.RetentionDays).Unix()

	for _, stmt := range []string{
		"DELETE FROM presence WHERE ts < ?",
		"DELETE FROM samples WHERE ts < ?",
	} {
		if _, err := s.db.Exec(stmt, cutoff); err != nil {
			s.log.WithError(err).Warn("Failed to prune expired rows")

			return
		}
	}

	if _, err := s.db.Exec("PRAGMA incremental_vacuum"); err != nil {
		s.log.WithError(err).Warn("Failed to reclaim database space")
	}
}
