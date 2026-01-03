// Package discord provides Discord bot functionality for status updates.
package discord

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/sirupsen/logrus"

	"github.com/samcm/ts-discord-status/internal/teamspeak"
)

// Config holds Discord bot settings.
type Config struct {
	Token     string
	ChannelID string
}

// DisplayConfig holds display formatting options.
type DisplayConfig struct {
	ShowEmptyChannels bool
	ServerAddress     string
	ServerPassword    string
	CustomFooter      string
	ChannelNameFormat string // e.g., "TS: {online}/{max}"
}

// Service defines the Discord service interface.
type Service interface {
	Start(ctx context.Context) error
	Stop() error
	UpdateStatus(ctx context.Context, state *teamspeak.State) error
}

type service struct {
	log               logrus.FieldLogger
	cfg               Config
	display           DisplayConfig
	session           *discordgo.Session
	messageID         string
	mu                sync.Mutex
	lastUserCount     int       // Track to avoid unnecessary renames
	lastChannelRename time.Time // Rate limit channel renames
}

// NewService creates a new Discord service.
func NewService(log logrus.FieldLogger, cfg Config, display DisplayConfig) Service {
	return &service{
		log:     log.WithField("component", "discord"),
		cfg:     cfg,
		display: display,
	}
}

// Start connects to Discord and finds or creates the status message.
func (s *service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, err := discordgo.New("Bot " + s.cfg.Token)
	if err != nil {
		return fmt.Errorf("failed to create Discord session: %w", err)
	}

	if err := session.Open(); err != nil {
		return fmt.Errorf("failed to open Discord connection: %w", err)
	}

	s.session = session
	s.log.Info("Connected to Discord")

	// Find existing message from this bot
	if err := s.findOrCreateMessage(); err != nil {
		s.session.Close()
		return fmt.Errorf("failed to find or create status message: %w", err)
	}

	return nil
}

// findOrCreateMessage searches for an existing message from this bot or creates a new one.
func (s *service) findOrCreateMessage() error {
	messages, err := s.session.ChannelMessages(s.cfg.ChannelID, 50, "", "", "")
	if err != nil {
		return fmt.Errorf("failed to fetch channel messages: %w", err)
	}

	botID := s.session.State.User.ID

	// Look for our own message
	for _, msg := range messages {
		if msg.Author.ID == botID && len(msg.Embeds) > 0 {
			s.messageID = msg.ID
			s.log.WithField("message_id", s.messageID).Info("Found existing status message")

			return nil
		}
	}

	// Create new message with placeholder
	embed := s.buildEmbed(nil)
	msg, err := s.session.ChannelMessageSendEmbed(s.cfg.ChannelID, embed)
	if err != nil {
		return fmt.Errorf("failed to create status message: %w", err)
	}

	s.messageID = msg.ID
	s.log.WithField("message_id", s.messageID).Info("Created new status message")

	return nil
}

// Stop disconnects from Discord.
func (s *service) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.session != nil {
		s.session.Close()
		s.session = nil
		s.log.Info("Disconnected from Discord")
	}

	return nil
}

// UpdateStatus updates the Discord message with the current TeamSpeak state.
func (s *service) UpdateStatus(ctx context.Context, state *teamspeak.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.session == nil {
		return fmt.Errorf("not connected to Discord")
	}

	embed := s.buildEmbed(state)

	_, err := s.session.ChannelMessageEditEmbed(s.cfg.ChannelID, s.messageID, embed)
	if err != nil {
		return fmt.Errorf("failed to update status message: %w", err)
	}

	// Update channel name if configured and conditions are met
	if s.display.ChannelNameFormat != "" && state != nil {
		s.maybeUpdateChannelName(state)
	}

	return nil
}

// maybeUpdateChannelName updates the channel name if user count changed and rate limit allows.
func (s *service) maybeUpdateChannelName(state *teamspeak.State) {
	// Only rename if user count changed
	if state.TotalUsers == s.lastUserCount {
		return
	}

	// Rate limit: minimum 5 minutes between renames (Discord allows 2 per 10 min)
	if time.Since(s.lastChannelRename) < 5*time.Minute {
		s.log.WithFields(logrus.Fields{
			"last_rename":  s.lastChannelRename,
			"next_allowed": s.lastChannelRename.Add(5 * time.Minute),
		}).Debug("Skipping channel rename due to rate limit")
		return
	}

	// Build new channel name from format
	newName := s.display.ChannelNameFormat
	newName = strings.ReplaceAll(newName, "{online}", fmt.Sprintf("%d", state.TotalUsers))
	newName = strings.ReplaceAll(newName, "{max}", fmt.Sprintf("%d", state.MaxClients))
	newName = strings.ReplaceAll(newName, "{server}", state.ServerName)

	// Update the channel
	_, err := s.session.ChannelEdit(s.cfg.ChannelID, &discordgo.ChannelEdit{
		Name: newName,
	})
	if err != nil {
		s.log.WithError(err).Warn("Failed to update channel name")
		return
	}

	s.lastUserCount = state.TotalUsers
	s.lastChannelRename = time.Now()
	s.log.WithField("name", newName).Info("Updated channel name")
}

// buildEmbed creates a Discord embed from the TeamSpeak state.
func (s *service) buildEmbed(state *teamspeak.State) *discordgo.MessageEmbed {
	embed := &discordgo.MessageEmbed{
		Title:     "TeamSpeak Status",
		Color:     0x2B5B84, // TeamSpeak blue
		Timestamp: time.Now().Format(time.RFC3339),
	}

	if state == nil {
		embed.Description = "Connecting to TeamSpeak server..."
		return embed
	}

	// Update title with server name
	embed.Title = fmt.Sprintf("TeamSpeak Status (%s)", state.ServerName)

	// Dynamic color based on activity
	if state.TotalUsers > 0 {
		embed.Color = 0x43B581 // Discord green - users online
	} else {
		embed.Color = 0x747F8D // Discord gray - empty
	}

	var fields []*discordgo.MessageEmbedField

	// Server info field (if configured)
	if s.display.ServerAddress != "" || s.display.ServerPassword != "" {
		var serverInfo strings.Builder

		if s.display.ServerAddress != "" {
			serverInfo.WriteString(fmt.Sprintf("**Address:** `%s`\n", s.display.ServerAddress))
		}

		if s.display.ServerPassword != "" {
			serverInfo.WriteString(fmt.Sprintf("**Password:** `%s`", s.display.ServerPassword))
		}

		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   "Server Info",
			Value:  serverInfo.String(),
			Inline: false,
		})
	}

	// Build channel list
	var channelContent strings.Builder

	for _, ch := range state.Channels {
		// Skip channels with no users if configured
		if !s.display.ShowEmptyChannels && len(ch.Users) == 0 {
			continue
		}

		// Skip spacer channels (usually named with [*spacer*] or similar)
		if strings.Contains(strings.ToLower(ch.Name), "spacer") {
			continue
		}

		channelContent.WriteString(fmt.Sprintf("**%s** (%d)\n", ch.Name, len(ch.Users)))

		for _, user := range ch.Users {
			status := buildUserStatus(user)
			if status != "" {
				channelContent.WriteString(fmt.Sprintf("  • %s %s\n", user.Nickname, status))
			} else {
				channelContent.WriteString(fmt.Sprintf("  • %s\n", user.Nickname))
			}
		}

		if len(ch.Users) > 0 {
			channelContent.WriteString("\n")
		}
	}

	if channelContent.Len() > 0 {
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   "Channels",
			Value:  channelContent.String(),
			Inline: false,
		})
	} else {
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   "Channels",
			Value:  "*No users online*",
			Inline: false,
		})
	}

	embed.Fields = fields

	// Footer with stats
	footerText := fmt.Sprintf("%d/%d online • Uptime: %s", state.TotalUsers, state.MaxClients, formatDuration(state.Uptime))

	if s.display.CustomFooter != "" {
		footerText += " • " + s.display.CustomFooter
	}

	embed.Footer = &discordgo.MessageEmbedFooter{
		Text: footerText,
	}

	return embed
}

// buildUserStatus creates a status string with icons for a user.
func buildUserStatus(user teamspeak.User) string {
	var status strings.Builder

	if user.IsRecording {
		status.WriteString("🔴")
	}

	if user.OutputMuted {
		status.WriteString("🔇") // Deafened (can't hear)
	} else if user.InputMuted {
		status.WriteString("🎙️") // Mic muted
	}

	if user.Away {
		status.WriteString("💤")
	}

	// Show idle time if > 5 minutes
	if user.IdleTime > 5*time.Minute {
		status.WriteString(fmt.Sprintf(" (%s idle)", formatIdleTime(user.IdleTime)))
	}

	return status.String()
}

// formatIdleTime formats idle duration in a compact way.
func formatIdleTime(d time.Duration) string {
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60

	if hours > 0 {
		return fmt.Sprintf("%dh%dm", hours, minutes)
	}

	return fmt.Sprintf("%dm", minutes)
}

// formatDuration formats a duration in a human-readable way.
func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}

	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}

	return fmt.Sprintf("%dm", minutes)
}
