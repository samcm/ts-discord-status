// Package teamspeak provides TeamSpeak ServerQuery client functionality.
package teamspeak

import "time"

// State represents the current state of the TeamSpeak server.
type State struct {
	ServerName string
	Uptime     time.Duration
	Channels   []Channel
	TotalUsers int
	MaxClients int
}

// Channel represents a TeamSpeak channel with its users.
type Channel struct {
	ID       int
	Name     string
	ParentID int
	Order    int
	Users    []User
}

// User represents a connected TeamSpeak client.
type User struct {
	ID          int
	Nickname    string
	ChannelID   int
	InputMuted  bool          // Microphone muted
	OutputMuted bool          // Speakers/headphones muted (deafened)
	Away        bool          // Away status
	AwayMessage string        // Away message
	IdleTime    time.Duration // How long they've been idle
	IsRecording bool          // Currently recording
}
