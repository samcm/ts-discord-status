# ts-discord-status

A minimal Go service that displays TeamSpeak server status in a Discord channel via an auto-updating embed message.

## Features

- Shows active TeamSpeak channels and users in Discord
- Auto-updates every 30 seconds (configurable)
- Persists message across restarts (finds its own message in the channel)
- Dry-run mode for testing without Discord
- Docker image with multi-arch support (amd64, arm64)

## Quick Start

### Using Docker

```bash
docker run -d \
  -v /path/to/config.yaml:/config.yaml \
  ghcr.io/samcm/ts-discord-status:latest \
  --config /config.yaml
```

### Using Binary

```bash
# Download from releases or build from source
go install github.com/samcm/ts-discord-status/cmd/ts-discord-status@latest

# Run
ts-discord-status --config config.yaml
```

### Dry Run (Test Mode)

Test your TeamSpeak connection without connecting to Discord:

```bash
ts-discord-status --config config.yaml --dry-run
```

## Configuration

Create a `config.yaml` file:

```yaml
teamspeak:
  host: "ts.example.com"
  query_port: 10011
  username: "serveradmin"
  password: "your-serverquery-password"
  server_id: 1

discord:
  token: "your-discord-bot-token"
  channel_id: "123456789012345678"

display:
  show_empty_channels: false
  update_interval: 30s
  server_info:
    address: "ts.example.com"
    password: "server-join-password"
  custom_footer: ""

logging:
  level: "info"
```

## Setup Guides

### Getting Discord Bot Token & Channel ID

1. **Enable Developer Mode in Discord**
   - Open Discord Settings
   - Go to App Settings → Advanced
   - Enable "Developer Mode"

2. **Create a Discord Bot**
   - Go to [Discord Developer Portal](https://discord.com/developers/applications)
   - Click "New Application" and give it a name
   - Go to the "Bot" section and click "Add Bot"
   - Click "Reset Token" and copy the token (this is your `discord.token`)

3. **Invite the Bot to Your Server**
   - Go to OAuth2 → URL Generator
   - Select the `bot` scope
   - Select permissions: `Send Messages`, `Read Message History`
   - Copy the generated URL and open it in your browser
   - Select your server and authorize

4. **Get Channel ID**
   - Right-click the channel where you want the status message
   - Click "Copy Channel ID"
   - This is your `discord.channel_id`

### Getting TeamSpeak ServerQuery Credentials

TeamSpeak ServerQuery is an administrative interface that runs on port 10011 by default.

1. **Default Credentials**
   - Username: `serveradmin`
   - Password: Generated on first server start (check server logs)

2. **Finding the Password**
   - Check your TeamSpeak server logs from first startup
   - Look for a line containing "serveradmin" and "password"
   - For Docker: `docker logs <container_name> | grep serveradmin`

3. **Resetting the Password (Docker)**
   ```bash
   # Stop and remove container (data persists in volume)
   docker stop teamspeak && docker rm teamspeak

   # Recreate with new password
   docker run -d --name teamspeak \
     -e TS3SERVER_LICENSE=accept \
     -p 9987:9987/udp -p 30033:30033 -p 10011:10011 \
     -v /your/data/path:/data \
     teamspeak \
     serveradmin_password=yournewpassword
   ```

4. **Resetting the Password (Native)**
   - Stop the TeamSpeak server
   - Run: `./ts3server serveradmin_password=newpassword`
   - Restart normally

## Discord Embed Preview

The bot will create and maintain a single message that looks like:

```
┌─────────────────────────────────┐
│ TeamSpeak Status                │
├─────────────────────────────────┤
│ Server: ts.example.com          │
│ Password: secret                │
├─────────────────────────────────┤
│ Lobby (2)                       │
│   • Alice                       │
│   • Bob                         │
│                                 │
│ Gaming (3)                      │
│   • Charlie                     │
│   • Dave                        │
│   • Eve                         │
├─────────────────────────────────┤
│ 5 users online • Uptime: 3d 4h  │
└─────────────────────────────────┘
```

## Building from Source

```bash
git clone https://github.com/samcm/ts-discord-status
cd ts-discord-status
go build -o ts-discord-status ./cmd/ts-discord-status
```

## License

MIT
