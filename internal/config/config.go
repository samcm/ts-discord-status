// Package config handles loading and validation of application configuration.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the complete application configuration.
type Config struct {
	TeamSpeak TeamSpeakConfig `yaml:"teamspeak"`
	Discord   DiscordConfig   `yaml:"discord"`
	Display   DisplayConfig   `yaml:"display"`
	Logging   LoggingConfig   `yaml:"logging"`
}

// TeamSpeakConfig holds TeamSpeak ServerQuery connection settings.
type TeamSpeakConfig struct {
	Host      string `yaml:"host"`
	QueryPort int    `yaml:"query_port"`
	Username  string `yaml:"username"`
	Password  string `yaml:"password"`
	ServerID  int    `yaml:"server_id"`
}

// DiscordConfig holds Discord bot settings.
type DiscordConfig struct {
	Token     string `yaml:"token"`
	ChannelID string `yaml:"channel_id"`
}

// DisplayConfig holds display and formatting options.
type DisplayConfig struct {
	ShowEmptyChannels bool          `yaml:"show_empty_channels"`
	UpdateInterval    time.Duration `yaml:"update_interval"`
	ServerInfo        ServerInfo    `yaml:"server_info"`
	CustomFooter      string        `yaml:"custom_footer"`
	ChannelNameFormat string        `yaml:"channel_name_format"` // e.g., "TS: {online}/{max}" - updates channel name
	ThumbnailURL      string        `yaml:"thumbnail_url"`       // Optional image URL for embed thumbnail
}

// ServerInfo holds optional server connection info to display.
type ServerInfo struct {
	Address  string `yaml:"address"`
	Password string `yaml:"password"`
}

// LoggingConfig holds logging settings.
type LoggingConfig struct {
	Level string `yaml:"level"`
}

// Load reads and parses the configuration from the given file path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	cfg := &Config{
		// Set defaults
		TeamSpeak: TeamSpeakConfig{
			QueryPort: 10011,
			Username:  "serveradmin",
			ServerID:  1,
		},
		Display: DisplayConfig{
			ShowEmptyChannels: false,
			UpdateInterval:    30 * time.Second,
		},
		Logging: LoggingConfig{
			Level: "info",
		},
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return cfg, nil
}

// Validate checks that all required configuration fields are set.
func (c *Config) Validate() error {
	if c.TeamSpeak.Host == "" {
		return fmt.Errorf("teamspeak.host is required")
	}

	if c.TeamSpeak.Password == "" {
		return fmt.Errorf("teamspeak.password is required")
	}

	if c.Discord.Token == "" {
		return fmt.Errorf("discord.token is required")
	}

	if c.Discord.ChannelID == "" {
		return fmt.Errorf("discord.channel_id is required")
	}

	if c.Display.UpdateInterval < 5*time.Second {
		return fmt.Errorf("display.update_interval must be at least 5s")
	}

	return nil
}
