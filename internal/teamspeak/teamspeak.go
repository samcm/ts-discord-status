package teamspeak

import (
	"context"
	"fmt"
	"sync"
	"time"

	ts3 "github.com/multiplay/go-ts3"
	"github.com/sirupsen/logrus"
)

// Config holds TeamSpeak connection settings.
type Config struct {
	Host      string
	QueryPort int
	Username  string
	Password  string
	ServerID  int
}

// Service defines the TeamSpeak service interface.
type Service interface {
	Start(ctx context.Context) error
	Stop() error
	GetState(ctx context.Context) (*State, error)
}

type service struct {
	log    logrus.FieldLogger
	cfg    Config
	client *ts3.Client
	mu     sync.Mutex
}

// NewService creates a new TeamSpeak service.
func NewService(log logrus.FieldLogger, cfg Config) Service {
	return &service{
		log: log.WithField("component", "teamspeak"),
		cfg: cfg,
	}
}

// Start connects to the TeamSpeak server.
func (s *service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.QueryPort)
	s.log.WithField("address", addr).Info("Connecting to TeamSpeak server")

	client, err := ts3.NewClient(addr)
	if err != nil {
		return fmt.Errorf("failed to connect to TeamSpeak: %w", err)
	}

	if err := client.Login(s.cfg.Username, s.cfg.Password); err != nil {
		client.Close()
		return fmt.Errorf("failed to authenticate: %w", err)
	}

	if err := client.Use(s.cfg.ServerID); err != nil {
		client.Close()
		return fmt.Errorf("failed to select virtual server %d: %w", s.cfg.ServerID, err)
	}

	s.client = client
	s.log.Info("Connected to TeamSpeak server")

	return nil
}

// Stop disconnects from the TeamSpeak server.
func (s *service) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.client != nil {
		s.client.Close()
		s.client = nil
		s.log.Info("Disconnected from TeamSpeak server")
	}

	return nil
}

// GetState fetches the current state of the TeamSpeak server.
func (s *service) GetState(ctx context.Context) (*State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.client == nil {
		return nil, fmt.Errorf("not connected to TeamSpeak server")
	}

	// Get server info
	server, err := s.client.Server.Info()
	if err != nil {
		return nil, fmt.Errorf("failed to get server info: %w", err)
	}

	// Get channels
	channels, err := s.client.Server.ChannelList()
	if err != nil {
		return nil, fmt.Errorf("failed to get channel list: %w", err)
	}

	// Get clients with extended info (voice, times, away status)
	clients, err := s.client.Server.ClientList(ts3.ClientVoice, ts3.ClientTimes, ts3.ClientAway)
	if err != nil {
		return nil, fmt.Errorf("failed to get client list: %w", err)
	}

	// Build channel map
	channelMap := make(map[int]*Channel, len(channels))
	stateChannels := make([]Channel, 0, len(channels))

	for _, ch := range channels {
		channel := Channel{
			ID:       ch.ID,
			Name:     ch.ChannelName,
			ParentID: ch.ParentID,
			Order:    ch.ChannelOrder,
			Users:    make([]User, 0),
		}
		channelMap[ch.ID] = &channel
		stateChannels = append(stateChannels, channel)
	}

	// Assign clients to channels
	totalUsers := 0
	for _, cl := range clients {
		// Skip ServerQuery clients
		if cl.Type == 1 {
			continue
		}

		user := User{
			ID:          cl.ID,
			Nickname:    cl.Nickname,
			ChannelID:   cl.ChannelID,
			Away:        cl.Away,
			AwayMessage: cl.AwayMessage,
		}

		// Populate voice status (if available)
		if cl.OnlineClientVoice != nil {
			if cl.InputMuted != nil {
				user.InputMuted = *cl.InputMuted
			}
			if cl.OutputMuted != nil {
				user.OutputMuted = *cl.OutputMuted
			}
			if cl.IsRecording != nil {
				user.IsRecording = *cl.IsRecording
			}
		}

		// Populate time info (if available)
		if cl.OnlineClientTimes != nil && cl.IdleTime != nil {
			user.IdleTime = time.Duration(*cl.IdleTime) * time.Millisecond
		}

		if ch, ok := channelMap[cl.ChannelID]; ok {
			ch.Users = append(ch.Users, user)
		}

		totalUsers++
	}

	// Update channels slice with populated users
	for i := range stateChannels {
		if ch, ok := channelMap[stateChannels[i].ID]; ok {
			stateChannels[i].Users = ch.Users
		}
	}

	state := &State{
		ServerName: server.Name,
		Uptime:     time.Duration(server.Uptime) * time.Second,
		Channels:   stateChannels,
		TotalUsers: totalUsers,
		MaxClients: server.MaxClients,
	}

	return state, nil
}
