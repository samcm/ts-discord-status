// Package api serves a read-only JSON summary of the latest TeamSpeak state.
//
// The response holds only whether the server is online, the client counts,
// the users per occupied channel with their mute, deafen, away and idle
// status, and when the state was last polled.
// It is served from the bridge's latest poll, so requests never reach
// TeamSpeak.
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/samcm/ts-discord-status/internal/teamspeak"
)

// Config holds API server settings.
type Config struct {
	Listen string // host:port to listen on
	// TokenSHA256 is the lowercase hex SHA-256 of the bearer token. Every
	// request must send "Authorization: Bearer <token>" whose hash matches;
	// any other request gets an empty 401. The server never holds the token.
	TokenSHA256 string
}

// Status is the JSON document served at /status.
type Status struct {
	Online      bool       `json:"online"`
	Clients     int        `json:"clients"`
	MaxClients  int        `json:"max_clients"`
	Channels    []Channel  `json:"channels"`
	LastUpdated *time.Time `json:"last_updated"`
}

// Channel lists the users in one occupied channel.
type Channel struct {
	Name  string `json:"name"`
	Users []User `json:"users"`
}

// User is one connected client as of the last poll. Talking is left out: a
// 30-second snapshot of TeamSpeak's talking flag misses nearly all speech.
type User struct {
	Name        string `json:"name"`
	InputMuted  bool   `json:"input_muted"`  // microphone muted
	OutputMuted bool   `json:"output_muted"` // speakers muted (deafened)
	Away        bool   `json:"away"`
	IdleS       int64  `json:"idle_s"` // seconds since the client's last activity
}

// Server serves the latest status over HTTP.
type Server struct {
	log    logrus.FieldLogger
	cfg    Config
	mu     sync.RWMutex
	status Status
	srv    *http.Server
}

// NewServer creates an API server. Call Start to begin listening.
func NewServer(log logrus.FieldLogger, cfg Config) *Server {
	s := &Server{
		log:    log.WithField("component", "api"),
		cfg:    cfg,
		status: Status{Channels: []Channel{}},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/status", s.handleStatus)

	s.srv = &http.Server{
		Handler:           s.requireToken(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}

	return s
}

// Update records the result of one poll. A nil state marks the server offline.
func (s *Server) Update(state *teamspeak.State, at time.Time) {
	at = at.UTC().Truncate(time.Second)

	s.mu.Lock()
	defer s.mu.Unlock()

	if state == nil {
		// Keep the last known maximum; it does not change when the server is down.
		s.status = Status{
			MaxClients:  s.status.MaxClients,
			Channels:    []Channel{},
			LastUpdated: &at,
		}

		return
	}

	channels := make([]Channel, 0)

	for _, ch := range state.Channels {
		if len(ch.Users) == 0 {
			continue
		}

		users := make([]User, 0, len(ch.Users))
		for _, u := range ch.Users {
			users = append(users, User{
				Name:        u.Nickname,
				InputMuted:  u.InputMuted,
				OutputMuted: u.OutputMuted,
				Away:        u.Away,
				IdleS:       int64(u.IdleTime / time.Second),
			})
		}

		channels = append(channels, Channel{Name: ch.Name, Users: users})
	}

	s.status = Status{
		Online:      true,
		Clients:     state.TotalUsers,
		MaxClients:  state.MaxClients,
		Channels:    channels,
		LastUpdated: &at,
	}
}

// Start begins listening in the background.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}

	go func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.WithError(err).Error("API server stopped")
		}
	}()

	s.log.WithField("listen", ln.Addr().String()).Info("API server started")

	return nil
}

// Stop shuts the server down.
func (s *Server) Stop(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)

		return
	}

	s.mu.RLock()
	body, err := json.Marshal(s.status)
	s.mu.RUnlock()

	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}

// requireToken answers 401 to every request, on any path or method, that
// lacks a bearer token matching TokenSHA256.
func (s *Server) requireToken(next http.Handler) http.Handler {
	want, err := hex.DecodeString(strings.ToLower(s.cfg.TokenSHA256))
	if err != nil || len(want) != sha256.Size {
		want = nil // misconfigured: reject everything
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		sum := sha256.Sum256([]byte(token))

		if !ok || token == "" || want == nil || subtle.ConstantTimeCompare(sum[:], want) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		next.ServeHTTP(w, r)
	})
}
