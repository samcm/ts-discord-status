// Package bridge orchestrates the TeamSpeak to Discord sync loop.
package bridge

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/samcm/ts-discord-status/internal/discord"
	"github.com/samcm/ts-discord-status/internal/teamspeak"
)

// Config holds bridge configuration.
type Config struct {
	UpdateInterval time.Duration
}

// Service defines the bridge service interface.
type Service interface {
	Start(ctx context.Context) error
	Stop() error
}

type service struct {
	log       logrus.FieldLogger
	cfg       Config
	teamspeak teamspeak.Service
	discord   discord.Service
	done      chan struct{}
	wg        sync.WaitGroup
}

// NewService creates a new bridge service.
func NewService(log logrus.FieldLogger, cfg Config, ts teamspeak.Service, dc discord.Service) Service {
	return &service{
		log:       log.WithField("component", "bridge"),
		cfg:       cfg,
		teamspeak: ts,
		discord:   dc,
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

	// Do initial update
	if err := s.update(ctx); err != nil {
		s.log.WithError(err).Warn("Initial update failed")
	}

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
			if err := s.update(ctx); err != nil {
				s.log.WithError(err).Warn("Update failed")
			}
		}
	}
}

// update fetches TeamSpeak state and updates Discord.
func (s *service) update(ctx context.Context) error {
	state, err := s.teamspeak.GetState(ctx)
	if err != nil {
		return fmt.Errorf("failed to get TeamSpeak state: %w", err)
	}

	s.log.WithField("users", state.TotalUsers).Debug("Fetched TeamSpeak state")

	if err := s.discord.UpdateStatus(ctx, state); err != nil {
		return fmt.Errorf("failed to update Discord status: %w", err)
	}

	return nil
}
