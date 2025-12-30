# ccc - Claude Code Companion

> Your companion for [Claude Code](https://claude.ai/claude-code) - control sessions remotely via Telegram. Start sessions from your phone, interact with Claude, and receive notifications when tasks complete.

[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

## Why ccc?

Ever wanted to:
- Start a Claude Code session from your phone while away from your computer?
- Continue a session seamlessly between your phone and PC?
- Get notified when Claude finishes a long-running task?

**ccc** bridges Claude Code with Telegram, letting you control sessions from anywhere.

## Features

- **100% Self-Hosted** - Runs entirely on your machine, no third-party servers
- **Privacy First** - Your code and conversations never leave your computer (except to Telegram for messages you send)
- **Remote Control** - Start and manage Claude Code sessions from Telegram
- **Multi-Session** - Run multiple concurrent sessions, each with its own Telegram topic
- **Seamless Handoff** - Start on phone, continue on PC (or vice versa)
- **Notifications** - Get Claude's responses in Telegram when away
- **tmux Integration** - Sessions persist and can be attached from any terminal
- **One-shot Queries** - Quick Claude questions via private chat

## Demo Workflow

```
📱 Phone (Telegram)              💻 PC (Terminal)
─────────────────────────────────────────────────────
1. /new myproject
   → Creates topic + session

2. "Fix the auth bug"
   → Claude starts working

3. Claude responds in topic
   ✅ myproject
   Fixed the auth bug by...

                                 4. cd ~/myproject && ccc
                                    → Attaches to same session

                                 5. Continue working with Claude
```

## Requirements

- macOS or Linux
- Go 1.21+
- [tmux](https://github.com/tmux/tmux)
- [Claude Code](https://claude.ai/claude-code) installed
- Telegram account

## Installation

### From Source

```bash
git clone https://github.com/kidandcat/ccc.git
cd ccc
go build -o ccc
sudo mv ccc /usr/local/bin/  # or ~/bin/
```

### Verify Installation

```bash
ccc --version
# ccc version 1.0.0
```

## Quick Start

### 1. Create a Telegram Bot

1. Open Telegram and message [@BotFather](https://t.me/botfather)
2. Send `/newbot` and follow the prompts
3. Save the bot token (looks like `123456789:ABCdefGHIjklMNOpqrsTUVwxyz`)

### 2. Initial Setup

```bash
ccc setup YOUR_BOT_TOKEN
# Then send any message to your bot in Telegram
```

### 3. Configure Group with Topics

For the best experience, create a Telegram group where each session gets its own topic:

1. Create a new Telegram group
2. Go to group settings → Enable "Topics"
3. Add your bot to the group and make it admin
4. Run:

```bash
ccc setgroup
# Send a message in the group to complete setup
```

### 4. Install Claude Hook

This enables Claude to send responses back to Telegram:

```bash
ccc install
```

### 5. Start the Bot Listener

```bash
ccc listen
```

> **Tip**: Run this as a service for 24/7 availability (see [Service Setup](#service-setup))

## Usage

### Terminal Commands

| Command | Description |
|---------|-------------|
| `ccc` | Start/attach Claude session in current directory |
| `ccc -c` | Continue previous session |
| `ccc "message"` | Send notification (if away mode on) |
| `ccc --help` | Show help |
| `ccc --version` | Show version |

### Telegram Commands

**In your group:**

| Command | Description |
|---------|-------------|
| `/new <name>` | Create new session + topic |
| `/new` | Restart session in current topic |
| `/kill <name>` | Kill a session |
| `/list` | List active sessions |
| `/ping` | Check if bot is alive |
| `/away` | Toggle away mode (notifications) |
| `/c <cmd>` | Run shell command on your machine |

**In private chat:**
- Send any message to run a one-shot Claude query

### Example Session

```bash
# On your PC - start working on a project
cd ~/myproject
ccc
# Claude session starts in tmux

# Later, from phone - check on progress
# Telegram: Send message in the myproject topic
# Claude responds in the topic

# Back on PC - continue where you left off
cd ~/myproject
ccc
# Attaches to existing session
```

## Service Setup

For the bot to run continuously, set it up as a system service.

<details>
<summary><strong>macOS (launchd)</strong></summary>

Create `~/Library/LaunchAgents/com.ccc.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.ccc</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/ccc</string>
        <string>listen</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/tmp/ccc.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/ccc.log</string>
</dict>
</plist>
```

Load the service:

```bash
launchctl load ~/Library/LaunchAgents/com.ccc.plist
```

</details>

<details>
<summary><strong>Linux (systemd)</strong></summary>

Create `~/.config/systemd/user/ccc.service`:

```ini
[Unit]
Description=Claude Code Controller
After=network.target

[Service]
ExecStart=/usr/local/bin/ccc listen
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
```

Enable and start:

```bash
systemctl --user enable ccc
systemctl --user start ccc
```

</details>

## Configuration

Config is stored in `~/.ccc.json`:

```json
{
  "bot_token": "your-telegram-bot-token",
  "chat_id": 123456789,
  "group_id": -1001234567890,
  "sessions": {
    "myproject": 42,
    "another-project": 43
  },
  "away": false
}
```

| Field | Description |
|-------|-------------|
| `bot_token` | Your Telegram bot token |
| `chat_id` | Your Telegram user ID (for authorization) |
| `group_id` | Telegram group ID for session topics |
| `sessions` | Map of session names to topic IDs |
| `away` | When true, notifications are sent |

## How It Works

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│  Telegram   │────▶│     ccc     │────▶│    tmux     │
│   (phone)   │◀────│   listen    │◀────│   session   │
└─────────────┘     └─────────────┘     └─────────────┘
                           │                   │
                           │                   ▼
                           │            ┌─────────────┐
                           └───────────▶│ Claude Code │
                              hook      └─────────────┘
```

1. `ccc listen` runs as a service, polling Telegram for messages
2. Messages in topics are forwarded to the corresponding tmux session
3. Claude Code runs inside tmux with a hook that sends responses back
4. You can attach to any session from terminal with `ccc`

## Privacy & Security

### Privacy

**ccc runs 100% on your machine.** There are no external servers, no analytics, no data collection.

- Your code stays on your computer
- Claude Code runs locally via Anthropic's official CLI
- Only messages you explicitly send go through Telegram
- No telemetry, no tracking, no cloud dependencies

The only external communication is:
1. **Telegram API** - For sending/receiving your messages (your bot, your control)
2. **Anthropic API** - Claude Code's own connection (handled by Claude Code itself)

### Security

- **Authorization**: Bot only accepts messages from the configured `chat_id`
- **Config permissions**: `~/.ccc.json` is created with `0600` (owner-only)
- **Open source**: Full code transparency, audit it yourself

> ⚠️ Note: Uses `--dangerously-skip-permissions` for automation - understand the implications

## Troubleshooting

**Bot not responding?**
- Check if `ccc listen` is running
- Verify bot token in `~/.ccc.json`
- Check logs: `tail -f /tmp/ccc.log`

**Session not starting?**
- Ensure tmux is installed: `which tmux`
- Check if Claude Code is installed: `which claude`

**Messages not reaching Claude?**
- Verify you're in the correct topic
- Check if session exists: `/list`
- Try restarting: `/new`

## Contributing

Contributions welcome! Please:

1. Fork the repository
2. Create a feature branch
3. Run tests: `go test ./...`
4. Submit a PR

## License

[MIT License](LICENSE) - feel free to use in your projects!

---

Made with Claude Code 🤖
