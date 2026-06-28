// Package bridge orchestrates the TeamSpeak to Discord sync loop.
package bridge

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/samcm/ts-discord-status/internal/discord"
	"github.com/samcm/ts-discord-status/internal/store"
	"github.com/samcm/ts-discord-status/internal/teamspeak"
)

// Config holds bridge configuration.
type Config struct {
	UpdateInterval time.Duration
	RecordInterval time.Duration
}

// Service defines the bridge service interface.
type Service interface {
	Start(ctx context.Context) error
	Stop() error
}

type service struct {
	log        logrus.FieldLogger
	cfg        Config
	teamspeak  teamspeak.Service
	discord    discord.Service
	store      store.Service
	lastRecord time.Time
	done       chan struct{}
	wg         sync.WaitGroup
}

// NewService creates a new bridge service. store may be nil to disable
// status recording.
func NewService(log logrus.FieldLogger, cfg Config, ts teamspeak.Service, dc discord.Service, st store.Service) Service {
	return &service{
		log:       log.WithField("component", "bridge"),
		cfg:       cfg,
		teamspeak: ts,
		discord:   dc,
		store:     st,
		done:      make(chan struct{}),
	}
}

// Start begins the sync loop.
func (s *service) Start(ctx context.Context) error {
	// Start TeamSpeak connection
	if err := s.teamspeak.Start(ctx); err != nil {
		return fmt.Errorf("failed to start TeamSpeak service: %w", err)
	}

	// Start Discord connection
	if err := s.discord.Start(ctx); err != nil {
		s.teamspeak.Stop()
		return fmt.Errorf("failed to start Discord service: %w", err)
	}

	// Start status recorder. A recording failure must never take down the bot,
	// so degrade to no recording rather than returning a fatal error.
	if s.store != nil {
		if err := s.store.Start(ctx); err != nil {
			s.log.WithError(err).Warn("Failed to start status recorder; continuing without recording")
			s.store = nil
		}
	}

	// Do initial update
	s.tick(ctx)

	// Start sync loop
	s.wg.Add(1)

	go s.loop(ctx)

	s.log.WithField("interval", s.cfg.UpdateInterval).Info("Bridge started")

	return nil
}

// Stop stops the sync loop and disconnects services.
func (s *service) Stop() error {
	close(s.done)
	s.wg.Wait()

	if s.store != nil {
		if err := s.store.Stop(); err != nil {
			s.log.WithError(err).Warn("Failed to stop store service")
		}
	}

	if err := s.discord.Stop(); err != nil {
		s.log.WithError(err).Warn("Failed to stop Discord service")
	}

	if err := s.teamspeak.Stop(); err != nil {
		s.log.WithError(err).Warn("Failed to stop TeamSpeak service")
	}

	s.log.Info("Bridge stopped")

	return nil
}

// loop runs the periodic update loop.
func (s *service) loop(ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(s.cfg.UpdateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

// tick fetches the current TeamSpeak state once and fans it out to Discord and,
// when due, the status recorder. A failure in one consumer does not block the
// other.
func (s *service) tick(ctx context.Context) {
	state, err := s.teamspeak.GetState(ctx)
	if err != nil {
		s.log.WithError(err).Warn("Failed to get TeamSpeak state")

		return
	}

	s.log.WithField("users", state.TotalUsers).Debug("Fetched TeamSpeak state")

	if err := s.discord.UpdateStatus(ctx, state); err != nil {
		s.log.WithError(err).Warn("Failed to update Discord status")
	}

	if s.store != nil && time.Since(s.lastRecord) >= s.cfg.RecordInterval {
		if err := s.store.Record(ctx, state); err != nil {
			s.log.WithError(err).Warn("Failed to record status snapshot")
		} else {
			s.lastRecord = time.Now()
		}
	}
}
