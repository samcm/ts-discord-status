package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/samcm/ts-discord-status/internal/teamspeak"
)

func newTestService(t *testing.T, retentionDays int) *service {
	t.Helper()

	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	svc := NewService(log, Config{
		Path:          filepath.Join(t.TempDir(), "status.db"),
		RetentionDays: retentionDays,
	}).(*service)

	require.NoError(t, svc.Start(context.Background()))
	t.Cleanup(func() { _ = svc.Stop() })

	return svc
}

func state(users ...string) *teamspeak.State {
	tsUsers := make([]teamspeak.User, 0, len(users))
	for _, n := range users {
		tsUsers = append(tsUsers, teamspeak.User{Nickname: n})
	}

	return &teamspeak.State{
		TotalUsers: len(users),
		MaxClients: 32,
		Uptime:     time.Hour,
		Channels:   []teamspeak.Channel{{Name: "General", Users: tsUsers}},
	}
}

func count(t *testing.T, svc *service, query string, args ...any) int {
	t.Helper()

	var n int
	require.NoError(t, svc.db.QueryRow(query, args...).Scan(&n))

	return n
}

func TestRecordSnapshot(t *testing.T) {
	svc := newTestService(t, 0)
	ctx := context.Background()
	now := time.Now().Unix()

	require.NoError(t, svc.recordAt(ctx, now, state("alice", "bob")))

	require.Equal(t, 1, count(t, svc, "SELECT COUNT(*) FROM samples"))
	require.Equal(t, 2, count(t, svc, "SELECT COUNT(*) FROM presence"))
	require.Equal(t, 2, count(t, svc, "SELECT COUNT(*) FROM users"))
	require.Equal(t, 2, count(t, svc, "SELECT total_users FROM samples"))
}

func TestRecordIsIdempotentWithinMinute(t *testing.T) {
	svc := newTestService(t, 0)
	ctx := context.Background()
	base := (time.Now().Unix() / 60) * 60

	// Two records inside the same minute bucket must not duplicate rows.
	require.NoError(t, svc.recordAt(ctx, base+5, state("alice", "bob")))
	require.NoError(t, svc.recordAt(ctx, base+40, state("alice", "bob")))

	require.Equal(t, 1, count(t, svc, "SELECT COUNT(*) FROM samples"))
	require.Equal(t, 2, count(t, svc, "SELECT COUNT(*) FROM presence"))

	// A later minute adds new rows; users stay de-duplicated.
	require.NoError(t, svc.recordAt(ctx, base+65, state("alice", "carol")))

	require.Equal(t, 2, count(t, svc, "SELECT COUNT(*) FROM samples"))
	require.Equal(t, 4, count(t, svc, "SELECT COUNT(*) FROM presence"))
	require.Equal(t, 3, count(t, svc, "SELECT COUNT(*) FROM users"))
}

func TestPruneDropsExpiredRows(t *testing.T) {
	svc := newTestService(t, 400)
	ctx := context.Background()

	old := time.Now().AddDate(0, 0, -500).Unix()
	recent := time.Now().Unix()

	require.NoError(t, svc.recordAt(ctx, old, state("ancient")))
	require.NoError(t, svc.recordAt(ctx, recent, state("alice")))

	svc.prune()

	require.Equal(t, 1, count(t, svc, "SELECT COUNT(*) FROM samples"))
	require.Equal(t, 1, count(t, svc, "SELECT COUNT(*) FROM presence"))
}
