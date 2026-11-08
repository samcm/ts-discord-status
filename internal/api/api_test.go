package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/samcm/ts-discord-status/internal/teamspeak"
)

func get(t *testing.T, s *Server, method, path, auth string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}

	rec := httptest.NewRecorder()
	s.srv.Handler.ServeHTTP(rec, req)

	return rec
}

func TestStatusServesOnlyTheAgreedFields(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	s := NewServer(log, Config{Token: "s3cret"})

	// Before the first poll.
	rec := get(t, s, http.MethodGet, "/status", "Bearer s3cret")
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"online":false,"clients":0,"max_clients":0,"channels":[],"last_updated":null}`, rec.Body.String())

	at := time.Date(2026, 11, 8, 9, 30, 15, 500, time.FixedZone("AEST", 10*3600))
	s.Update(&teamspeak.State{
		ServerName: "secret server name",
		Uptime:     time.Hour,
		TotalUsers: 3,
		MaxClients: 32,
		Channels: []teamspeak.Channel{
			{ID: 1, Name: "Lobby", Users: []teamspeak.User{
				{ID: 7, Nickname: "alice", AwayMessage: "brb", Away: true, InputMuted: true},
				{ID: 9, Nickname: "bob"},
			}},
			{ID: 2, Name: "Empty"},
			{ID: 3, Name: "Raid", Users: []teamspeak.User{{ID: 11, Nickname: "carol", IsRecording: true}}},
		},
	}, at)

	rec = get(t, s, http.MethodGet, "/status", "Bearer s3cret")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	require.JSONEq(t, `{
		"online": true,
		"clients": 3,
		"max_clients": 32,
		"channels": [
			{"name": "Lobby", "users": ["alice", "bob"]},
			{"name": "Raid", "users": ["carol"]}
		],
		"last_updated": "2026-11-07T23:30:15Z"
	}`, rec.Body.String())

	// A failed poll reports offline but keeps the known maximum.
	s.Update(nil, at.Add(30*time.Second))
	rec = get(t, s, http.MethodGet, "/status", "Bearer s3cret")
	require.JSONEq(t, `{"online":false,"clients":0,"max_clients":32,"channels":[],"last_updated":"2026-11-07T23:30:45Z"}`, rec.Body.String())
}

func TestStatusRequiresTokenAndIsReadOnly(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.PanicLevel)

	s := NewServer(log, Config{Token: "s3cret"})

	for _, auth := range []string{"", "Bearer wrong", "s3cret", "Bearer s3cret2", "Basic s3cret"} {
		rec := get(t, s, http.MethodGet, "/status", auth)
		require.Equal(t, http.StatusUnauthorized, rec.Code, "auth %q", auth)
		require.Empty(t, rec.Body.String())
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := get(t, s, method, "/status", "Bearer s3cret")
		require.Equal(t, http.StatusMethodNotAllowed, rec.Code, method)
		require.Empty(t, rec.Body.String())
	}

	require.Equal(t, http.StatusNotFound, get(t, s, http.MethodGet, "/", "Bearer s3cret").Code)
	require.Equal(t, http.StatusNotFound, get(t, s, http.MethodGet, "/status/x", "Bearer s3cret").Code)

	// Without a token configured, the endpoint is open.
	open := NewServer(log, Config{})
	require.Equal(t, http.StatusOK, get(t, open, http.MethodGet, "/status", "").Code)
}
