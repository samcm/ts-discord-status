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
	ThumbnailURL      string // Optional thumbnail image URL
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
		Color:     0x2B5B84, // TeamSpeak blue
		Timestamp: time.Now().Format(time.RFC3339),
		Author: &discordgo.MessageEmbedAuthor{
			Name:    "TeamSpeak Server",
			IconURL: "https://i.imgur.com/pK2qRkC.png", // TS3 icon
		},
	}

	if state == nil {
		embed.Description = "```\n⏳ Connecting to server...\n```"
		embed.Color = 0xFAA61A // Orange - connecting
		return embed
	}

	// Server name as title
	embed.Title = state.ServerName

	// Optional thumbnail
	if s.display.ThumbnailURL != "" {
		embed.Thumbnail = &discordgo.MessageEmbedThumbnail{
			URL: s.display.ThumbnailURL,
		}
	}

	// Dynamic color based on capacity
	capacityPercent := float64(state.TotalUsers) / float64(state.MaxClients)
	switch {
	case state.TotalUsers == 0:
		embed.Color = 0x95A5A6 // Gray - empty
	case capacityPercent >= 0.8:
		embed.Color = 0xE74C3C // Red - almost full
	case capacityPercent >= 0.5:
		embed.Color = 0xF39C12 // Orange - busy
	default:
		embed.Color = 0x2ECC71 // Green - available
	}

	var fields []*discordgo.MessageEmbedField

	// Stats row (inline fields)
	fields = append(fields, &discordgo.MessageEmbedField{
		Name:   "👥 Online",
		Value:  fmt.Sprintf("**%d** / %d", state.TotalUsers, state.MaxClients),
		Inline: true,
	})

	fields = append(fields, &discordgo.MessageEmbedField{
		Name:   "⏱️ Uptime",
		Value:  formatDuration(state.Uptime),
		Inline: true,
	})

	// Connection info (if configured)
	if s.display.ServerAddress != "" {
		connectValue := fmt.Sprintf("`%s`", s.display.ServerAddress)
		if s.display.ServerPassword != "" {
			connectValue += fmt.Sprintf("\nPass: `%s`", s.display.ServerPassword)
		}

		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   "🔗 Connect",
			Value:  connectValue,
			Inline: true,
		})
	}

	// Build channel list with better formatting
	channelContent := s.buildChannelList(state)
	if channelContent != "" {
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   "📢 Channels",
			Value:  channelContent,
			Inline: false,
		})
	}

	embed.Fields = fields

	// Clean footer
	footerText := "Last updated"
	if s.display.CustomFooter != "" {
		footerText = s.display.CustomFooter
	}

	embed.Footer = &discordgo.MessageEmbedFooter{
		Text: footerText,
	}

	return embed
}

// buildChannelList formats the channel and user list.
func (s *service) buildChannelList(state *teamspeak.State) string {
	var content strings.Builder

	hasContent := false

	for _, ch := range state.Channels {
		// Skip channels with no users if configured
		if !s.display.ShowEmptyChannels && len(ch.Users) == 0 {
			continue
		}

		// Skip spacer channels
		if strings.Contains(strings.ToLower(ch.Name), "spacer") {
			continue
		}

		hasContent = true

		// Channel header with user count
		if len(ch.Users) > 0 {
			content.WriteString(fmt.Sprintf("**#%s** `%d`\n", ch.Name, len(ch.Users)))
		} else {
			content.WriteString(fmt.Sprintf("**#%s**\n", ch.Name))
		}

		// User list
		for _, user := range ch.Users {
			status := buildUserStatus(user)
			if status != "" {
				content.WriteString(fmt.Sprintf("ㅤ• %s %s\n", user.Nickname, status))
			} else {
				content.WriteString(fmt.Sprintf("ㅤ• %s\n", user.Nickname))
			}
		}

		content.WriteString("\n")
	}

	if !hasContent {
		return "*No active channels*"
	}

	return strings.TrimRight(content.String(), "\n")
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
