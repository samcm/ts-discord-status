// Package main provides the entry point for ts-discord-status.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // embed the timezone database for the recap in distroless

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/samcm/ts-discord-status/internal/bridge"
	"github.com/samcm/ts-discord-status/internal/config"
	"github.com/samcm/ts-discord-status/internal/discord"
	"github.com/samcm/ts-discord-status/internal/store"
	"github.com/samcm/ts-discord-status/internal/teamspeak"
)

var (
	configPath string
	dryRun     bool
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "ts-discord-status",
	Short: "Display TeamSpeak server status in Discord",
	Long:  "A minimal service that displays TeamSpeak server status in a Discord channel via an auto-updating embed message.",
	RunE:  run,
}

func init() {
	rootCmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to configuration file (required)")
	rootCmd.Flags().BoolVar(&dryRun, "dry-run", false, "Fetch TeamSpeak state and print what would be posted, without connecting to Discord")

	rootCmd.MarkFlagRequired("config")
}

func run(cmd *cobra.Command, args []string) error {
	// Load configuration
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	// Setup logger
	log := logrus.New()

	level, err := logrus.ParseLevel(cfg.Logging.Level)
	if err != nil {
		return fmt.Errorf("invalid log level %q: %w", cfg.Logging.Level, err)
	}

	log.SetLevel(level)
	log.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})

	// Create TeamSpeak service
	tsService := teamspeak.NewService(log, teamspeak.Config{
		Host:      cfg.TeamSpeak.Host,
		QueryPort: cfg.TeamSpeak.QueryPort,
		Username:  cfg.TeamSpeak.Username,
		Password:  cfg.TeamSpeak.Password,
		ServerID:  cfg.TeamSpeak.ServerID,
	})

	if dryRun {
		return runDryRun(cmd.Context(), log, tsService, cfg)
	}

	// Create Discord service
	dcService := discord.NewService(log, discord.Config{
		Token:     cfg.Discord.Token,
		ChannelID: cfg.Discord.ChannelID,
	}, discord.DisplayConfig{
		ShowEmptyChannels: cfg.Display.ShowEmptyChannels,
		ServerAddress:     cfg.Display.ServerInfo.Address,
		ServerPassword:    cfg.Display.ServerInfo.Password,
		CustomFooter:      cfg.Display.CustomFooter,
		ChannelNameFormat: cfg.Display.ChannelNameFormat,
		ThumbnailURL:      cfg.Display.ThumbnailURL,
	})

	// Create status recorder (optional)
	var storeService store.Service
	if cfg.Database.Enabled {
		storeService = store.NewService(log, store.Config{
			Path:          cfg.Database.Path,
			RetentionDays: cfg.Database.RetentionDays,
		})
	}

	// Create bridge service
	bridgeService := bridge.NewService(log, bridge.Config{
		UpdateInterval: cfg.Display.UpdateInterval,
		RecordInterval: cfg.Database.RecordInterval,
	}, tsService, dcService, storeService)

	// Setup context with signal handling
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Info("Received shutdown signal")
		cancel()
	}()

	// Start bridge
	if err := bridgeService.Start(ctx); err != nil {
		return fmt.Errorf("failed to start bridge: %w", err)
	}

	// Wait for context cancellation
	<-ctx.Done()

	// Stop bridge
	if err := bridgeService.Stop(); err != nil {
		log.WithError(err).Warn("Error stopping bridge")
	}

	log.Info("Shutdown complete")

	return nil
}

// runDryRun fetches TeamSpeak state and prints what would be posted to Discord.
func runDryRun(ctx context.Context, log logrus.FieldLogger, ts teamspeak.Service, cfg *config.Config) error {
	log.Info("Running in dry-run mode")

	// Connect to TeamSpeak
	if err := ts.Start(ctx); err != nil {
		return fmt.Errorf("failed to connect to TeamSpeak: %w", err)
	}

	defer ts.Stop()

	// Fetch state
	state, err := ts.GetState(ctx)
	if err != nil {
		return fmt.Errorf("failed to get TeamSpeak state: %w", err)
	}

	// Print state
	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════════════════════╗")
	title := fmt.Sprintf("TeamSpeak Status (%s)", state.ServerName)
	padding := (62 - len(title)) / 2
	fmt.Printf("║%s%s%s║\n", strings.Repeat(" ", padding), title, strings.Repeat(" ", 62-padding-len(title)))
	fmt.Println("╠══════════════════════════════════════════════════════════════╣")

	if cfg.Display.ServerInfo.Address != "" || cfg.Display.ServerInfo.Password != "" {
		if cfg.Display.ServerInfo.Address != "" {
			fmt.Printf("║  Address: %-52s ║\n", cfg.Display.ServerInfo.Address)
		}

		if cfg.Display.ServerInfo.Password != "" {
			fmt.Printf("║  Password: %-51s ║\n", cfg.Display.ServerInfo.Password)
		}

		fmt.Println("╠══════════════════════════════════════════════════════════════╣")
	}

	hasUsers := false

	for _, ch := range state.Channels {
		if !cfg.Display.ShowEmptyChannels && len(ch.Users) == 0 {
			continue
		}

		if strings.Contains(strings.ToLower(ch.Name), "spacer") {
			continue
		}

		hasUsers = true
		fmt.Printf("║  📁 %-55s (%d) ║\n", truncate(ch.Name, 50), len(ch.Users))

		for _, user := range ch.Users {
			status := buildUserStatusCLI(user)
			if status != "" {
				display := fmt.Sprintf("%s %s", user.Nickname, status)
				fmt.Printf("║      • %-55s ║\n", truncate(display, 50))
			} else {
				fmt.Printf("║      • %-55s ║\n", truncate(user.Nickname, 50))
			}
		}
	}

	if !hasUsers {
		fmt.Println("║  No users online                                             ║")
	}

	fmt.Println("╠══════════════════════════════════════════════════════════════╣")
	fmt.Printf("║  %d/%d online • Uptime: %-38s ║\n", state.TotalUsers, state.MaxClients, formatDuration(state.Uptime))

	if cfg.Display.CustomFooter != "" {
		fmt.Printf("║  %-60s ║\n", truncate(cfg.Display.CustomFooter, 60))
	}

	fmt.Println("╚══════════════════════════════════════════════════════════════╝")
	fmt.Println()

	return nil
}

func buildUserStatusCLI(user teamspeak.User) string {
	var parts []string

	if user.IsRecording {
		parts = append(parts, "🔴REC")
	}

	if user.OutputMuted {
		parts = append(parts, "🔇")
	} else if user.InputMuted {
		parts = append(parts, "🎙️")
	}

	if user.Away {
		if user.AwayMessage != "" {
			parts = append(parts, fmt.Sprintf("💤(%s)", user.AwayMessage))
		} else {
			parts = append(parts, "💤")
		}
	}

	// Show idle time if > 5 minutes
	if user.IdleTime > 5*time.Minute {
		hours := int(user.IdleTime.Hours())
		minutes := int(user.IdleTime.Minutes()) % 60
		if hours > 0 {
			parts = append(parts, fmt.Sprintf("idle %dh%dm", hours, minutes))
		} else {
			parts = append(parts, fmt.Sprintf("idle %dm", minutes))
		}
	}

	return strings.Join(parts, " ")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}

	return s[:max-3] + "..."
}

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
