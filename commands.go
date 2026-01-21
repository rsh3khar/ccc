package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Execute shell command
func executeCommand(cmdStr string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "zsh", "-i", "-l", "-c", cmdStr)
	cmd.Dir, _ = os.UserHomeDir()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderr.String()
	}

	if output == "" {
		if err != nil {
			output = fmt.Sprintf("Error: %v", err)
		} else {
			output = "(no output)"
		}
	}

	return strings.TrimSpace(output), err
}

// One-shot Claude run (for private chat)
func runClaude(prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	home, _ := os.UserHomeDir()
	workDir := home

	words := strings.Fields(prompt)
	if len(words) > 0 {
		firstWord := words[0]
		potentialDir := filepath.Join(home, firstWord)
		if info, err := os.Stat(potentialDir); err == nil && info.IsDir() {
			workDir = potentialDir
			prompt = strings.TrimSpace(strings.TrimPrefix(prompt, firstWord))
			if prompt == "" {
				return "Error: no prompt provided after directory name", nil
			}
		}
	}

	if claudePath == "" {
		return "Error: claude binary not found", fmt.Errorf("claude not found")
	}
	cmd := exec.CommandContext(ctx, claudePath, "--dangerously-skip-permissions", "-p", prompt)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderr.String()
	}

	if output == "" {
		if err != nil {
			output = fmt.Sprintf("Error: %v", err)
		} else {
			output = "(no output)"
		}
	}

	return strings.TrimSpace(output), err
}

func setup(botToken string) error {
	fmt.Println("🚀 Claude Code Companion Setup")
	fmt.Println("==============================")
	fmt.Println()

	config := &Config{BotToken: botToken, Sessions: make(map[string]*SessionInfo)}

	// Step 1: Get chat ID
	fmt.Println("Step 1/4: Connecting to Telegram...")
	fmt.Println("📱 Send any message to your bot in Telegram")
	fmt.Println("   Waiting...")

	offset := 0
	for {
		resp, err := http.Get(fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?offset=%d&timeout=30", botToken, offset))
		if err != nil {
			return fmt.Errorf("failed to get updates: %w", err)
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var updates TelegramUpdate
		if err := json.Unmarshal(body, &updates); err != nil {
			return fmt.Errorf("failed to parse response: %w", err)
		}

		if !updates.OK {
			return fmt.Errorf("telegram API error - check your bot token")
		}

		for _, update := range updates.Result {
			offset = update.UpdateID + 1
			if update.Message.Chat.ID != 0 {
				config.ChatID = update.Message.Chat.ID
				if err := saveConfig(config); err != nil {
					return fmt.Errorf("failed to save config: %w", err)
				}
				fmt.Printf("✅ Connected! (User: @%s)\n\n", update.Message.From.Username)
				goto step2
			}
		}

		time.Sleep(time.Second)
	}

step2:
	// Step 2: Group setup (optional)
	fmt.Println("Step 2/4: Group setup (optional)")
	fmt.Println("   For session topics, create a Telegram group with Topics enabled,")
	fmt.Println("   add your bot as admin, and send a message there.")
	fmt.Println("   Or press Enter to skip...")

	// Non-blocking check for group message with timeout
	fmt.Println("   Waiting 30 seconds for group message...")

	client := &http.Client{Timeout: 35 * time.Second}
	deadline := time.Now().Add(30 * time.Second)

	for time.Now().Before(deadline) {
		reqURL := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?offset=%d&timeout=5", config.BotToken, offset)
		resp, err := client.Get(reqURL)
		if err != nil {
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var updates TelegramUpdate
		json.Unmarshal(body, &updates)

		for _, update := range updates.Result {
			offset = update.UpdateID + 1
			chat := update.Message.Chat
			if chat.Type == "supergroup" {
				config.GroupID = chat.ID
				saveConfig(config)
				fmt.Printf("✅ Group configured!\n\n")
				goto step3
			}
		}
	}
	fmt.Println("⏭️  Skipped (you can run 'ccc setgroup' later)")

step3:
	// Step 3: Install Claude hook and skill
	fmt.Println("Step 3/4: Installing Claude hook and skill...")
	if err := installHook(); err != nil {
		fmt.Printf("⚠️  Hook installation failed: %v\n", err)
		fmt.Println("   You can install it later with: ccc install")
	}
	if err := installSkill(); err != nil {
		fmt.Printf("⚠️  Skill installation failed: %v\n", err)
	} else {
		fmt.Println()
	}

	// Step 4: Install service
	fmt.Println("Step 4/4: Installing background service...")
	if err := installService(); err != nil {
		fmt.Printf("⚠️  Service installation failed: %v\n", err)
		fmt.Println("   You can start manually with: ccc listen")
	} else {
		fmt.Println()
	}

	// Done!
	fmt.Println("==============================")
	fmt.Println("✅ Setup complete!")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  ccc           Start Claude Code in current directory")
	fmt.Println("  ccc -c        Continue previous session")
	fmt.Println()
	if config.GroupID != 0 {
		fmt.Println("Telegram commands (in your group):")
		fmt.Println("  /new <name>   Create new session")
		fmt.Println("  /list         List sessions")
	} else {
		fmt.Println("To enable Telegram session topics:")
		fmt.Println("  1. Create a group with Topics enabled")
		fmt.Println("  2. Add bot as admin")
		fmt.Println("  3. Run: ccc setgroup")
	}

	return nil
}

func setGroup(config *Config) error {
	fmt.Println("Send a message in the group where you want to use topics...")
	fmt.Println("(Make sure Topics are enabled in group settings)")

	offset := 0
	client := &http.Client{Timeout: 35 * time.Second}

	for {
		reqURL := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?offset=%d&timeout=30", config.BotToken, offset)
		resp, err := client.Get(reqURL)
		if err != nil {
			return err
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var updates TelegramUpdate
		if err := json.Unmarshal(body, &updates); err != nil {
			continue
		}

		for _, update := range updates.Result {
			offset = update.UpdateID + 1
			chat := update.Message.Chat
			if chat.Type == "supergroup" && update.Message.From.ID == config.ChatID {
				config.GroupID = chat.ID
				if err := saveConfig(config); err != nil {
					return err
				}
				fmt.Printf("Group set: %d\n", chat.ID)
				fmt.Println("You can now create sessions with: /new <name>")
				return nil
			}
		}
	}
}

func doctor() {
	fmt.Println("🩺 ccc doctor")
	fmt.Println("=============")
	fmt.Println()

	allGood := true

	// Check tmux
	fmt.Print("tmux.............. ")
	if tmuxPath != "" {
		fmt.Printf("✅ %s\n", tmuxPath)
	} else {
		fmt.Println("❌ not found")
		fmt.Println("   Install: brew install tmux (macOS) or apt install tmux (Linux)")
		allGood = false
	}

	// Check claude
	fmt.Print("claude............ ")
	if claudePath != "" {
		fmt.Printf("✅ %s\n", claudePath)
	} else {
		fmt.Println("❌ not found")
		fmt.Println("   Install: npm install -g @anthropic-ai/claude-code")
		allGood = false
	}

	// Check ccc is in ~/bin (for hooks)
	fmt.Print("ccc in ~/bin...... ")
	home, _ := os.UserHomeDir()
	expectedCccPath := filepath.Join(home, "bin", "ccc")
	if _, err := os.Stat(expectedCccPath); err == nil {
		fmt.Printf("✅ %s\n", expectedCccPath)
	} else {
		fmt.Println("❌ not found")
		fmt.Println("   Run: mkdir -p ~/bin && cp ccc ~/bin/")
		allGood = false
	}

	// Check config
	fmt.Print("config............ ")
	config, err := loadConfig()
	if err != nil {
		fmt.Println("❌ not found")
		fmt.Println("   Run: ccc setup <bot_token>")
		allGood = false
	} else {
		fmt.Printf("✅ %s\n", getConfigPath())

		// Check bot token
		fmt.Print("  bot_token....... ")
		if config.BotToken != "" {
			fmt.Println("✅ configured")
		} else {
			fmt.Println("❌ missing")
			allGood = false
		}

		// Check chat ID
		fmt.Print("  chat_id......... ")
		if config.ChatID != 0 {
			fmt.Printf("✅ %d\n", config.ChatID)
		} else {
			fmt.Println("❌ missing")
			allGood = false
		}

		// Check group ID (optional)
		fmt.Print("  group_id........ ")
		if config.GroupID != 0 {
			fmt.Printf("✅ %d\n", config.GroupID)
		} else {
			fmt.Println("⚠️  not set (optional, run: ccc setgroup)")
		}
	}

	// Check Claude hook
	fmt.Print("claude hook....... ")
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	if data, err := os.ReadFile(settingsPath); err == nil {
		var settings map[string]interface{}
		if json.Unmarshal(data, &settings) == nil {
			if hooks, ok := settings["hooks"].(map[string]interface{}); ok {
				if _, hasStop := hooks["Stop"]; hasStop {
					fmt.Println("✅ installed")
				} else {
					fmt.Println("❌ not installed")
					fmt.Println("   Run: ccc install")
					allGood = false
				}
			} else {
				fmt.Println("❌ not installed")
				fmt.Println("   Run: ccc install")
				allGood = false
			}
		} else {
			fmt.Println("⚠️  settings.json parse error")
		}
	} else {
		fmt.Println("⚠️  ~/.claude/settings.json not found")
	}

	// Check service
	fmt.Print("service........... ")
	if _, err := os.Stat("/Library"); err == nil {
		// macOS - check launchd
		plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.ccc.plist")
		if _, err := os.Stat(plistPath); err == nil {
			// Check if loaded
			cmd := exec.Command("launchctl", "list", "com.ccc")
			if cmd.Run() == nil {
				fmt.Println("✅ running (launchd)")
			} else {
				fmt.Println("⚠️  installed but not running")
				fmt.Println("   Run: launchctl load ~/Library/LaunchAgents/com.ccc.plist")
			}
		} else {
			fmt.Println("❌ not installed")
			fmt.Println("   Run: ccc setup <token> (or manually create plist)")
			allGood = false
		}
	} else {
		// Linux - check systemd
		cmd := exec.Command("systemctl", "--user", "is-active", "ccc")
		if output, err := cmd.Output(); err == nil && strings.TrimSpace(string(output)) == "active" {
			fmt.Println("✅ running (systemd)")
		} else {
			servicePath := filepath.Join(home, ".config", "systemd", "user", "ccc.service")
			if _, err := os.Stat(servicePath); err == nil {
				fmt.Println("⚠️  installed but not running")
				fmt.Println("   Run: systemctl --user start ccc")
			} else {
				fmt.Println("❌ not installed")
				fmt.Println("   Run: ccc setup <token> (or manually create service)")
				allGood = false
			}
		}
	}

	// Check transcription (optional)
	fmt.Print("transcription..... ")
	if config != nil && config.TranscriptionCmd != "" {
		cmdPath := expandPath(config.TranscriptionCmd)
		if _, err := os.Stat(cmdPath); err == nil {
			fmt.Printf("✅ %s\n", cmdPath)
		} else if _, err := exec.LookPath(config.TranscriptionCmd); err == nil {
			fmt.Printf("✅ %s (in PATH)\n", config.TranscriptionCmd)
		} else {
			fmt.Printf("❌ %s not found\n", config.TranscriptionCmd)
			fmt.Println("   Check transcription_cmd in ~/.ccc.json")
		}
	} else if whisperPath, err := exec.LookPath("whisper"); err == nil {
		fmt.Printf("✅ %s (fallback)\n", whisperPath)
	} else if _, err := os.Stat("/opt/homebrew/bin/whisper"); err == nil {
		fmt.Println("✅ /opt/homebrew/bin/whisper (fallback)")
	} else {
		fmt.Println("⚠️  not configured (optional, for voice messages)")
		fmt.Println("   Set transcription_cmd in ~/.ccc.json or install whisper")
	}

	fmt.Println()
	if allGood {
		fmt.Println("✅ All checks passed!")
	} else {
		fmt.Println("❌ Some issues found. Fix them and run 'ccc doctor' again.")
	}
}

// Send notification (only if away)
func send(message string) error {
	config, err := loadConfig()
	if err != nil {
		return fmt.Errorf("not configured. Run: ccc setup <bot_token>")
	}

	if !config.Away {
		fmt.Println("Away mode off, skipping notification.")
		return nil
	}

	// Try to send to session topic if we're in a session directory
	if config.GroupID != 0 {
		cwd, _ := os.Getwd()
		for name, info := range config.Sessions {
			if info == nil {
				continue
			}
			// Match against saved path, subdirectories of saved path, or suffix
			if cwd == info.Path || strings.HasPrefix(cwd, info.Path+"/") || strings.HasSuffix(cwd, "/"+name) {
				return sendMessage(config, config.GroupID, info.TopicID, message)
			}
		}
	}

	// Fallback to private chat
	return sendMessage(config, config.ChatID, 0, message)
}

// Main listen loop
func listen() error {
	// Kill any other ccc listen instances to avoid Telegram API conflicts
	myPid := os.Getpid()
	cmd := exec.Command("pgrep", "-f", "ccc listen")
	output, _ := cmd.Output()
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if pid, err := strconv.Atoi(line); err == nil && pid != myPid {
			syscall.Kill(pid, syscall.SIGTERM)
		}
	}

	config, err := loadConfig()
	if err != nil {
		return fmt.Errorf("not configured. Run: ccc setup <bot_token>")
	}

	fmt.Printf("Bot listening... (chat: %d, group: %d)\n", config.ChatID, config.GroupID)
	fmt.Printf("Active sessions: %d\n", len(config.Sessions))
	fmt.Println("Press Ctrl+C to stop")

	setBotCommands(config.BotToken)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	offset := 0
	client := &http.Client{Timeout: 35 * time.Second}

	go func() {
		<-sigChan
		fmt.Println("\nShutting down...")
		os.Exit(0)
	}()

	for {
		reqURL := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?offset=%d&timeout=30", config.BotToken, offset)
		resp, err := client.Get(reqURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Network error: %v (retrying...)\n", err)
			time.Sleep(5 * time.Second)
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var updates TelegramUpdate
		if err := json.Unmarshal(body, &updates); err != nil {
			fmt.Fprintf(os.Stderr, "Parse error: %v\n", err)
			time.Sleep(time.Second)
			continue
		}

		if !updates.OK {
			fmt.Fprintf(os.Stderr, "Telegram API error: %s\n", updates.Description)
			time.Sleep(5 * time.Second)
			continue
		}

		for _, update := range updates.Result {
			offset = update.UpdateID + 1

			// Handle callback queries (button presses)
			if update.CallbackQuery != nil {
				cb := update.CallbackQuery
				// Only accept from authorized user
				if cb.From.ID != config.ChatID {
					continue
				}

				answerCallbackQuery(config, cb.ID)

				// Parse callback data: session:questionIndex:optionIndex
				parts := strings.Split(cb.Data, ":")
				if len(parts) == 3 {
					sessionName := parts[0]
					// questionIndex := parts[1] // for multi-question support
					optionIndex, _ := strconv.Atoi(parts[2])

					// Edit message to show selection and remove buttons
					if cb.Message != nil {
						originalText := cb.Message.Text
						newText := fmt.Sprintf("%s\n\n✓ Selected option %d", originalText, optionIndex+1)
						editMessageRemoveKeyboard(config, cb.Message.Chat.ID, cb.Message.MessageID, newText)
					}

					tmuxName := "claude-" + sessionName
					if tmuxSessionExists(tmuxName) {
						// Send arrow down keys to select option, then Enter
						for i := 0; i < optionIndex; i++ {
							exec.Command(tmuxPath, "send-keys", "-t", tmuxName, "Down").Run()
							time.Sleep(50 * time.Millisecond)
						}
						exec.Command(tmuxPath, "send-keys", "-t", tmuxName, "Enter").Run()
						fmt.Printf("[callback] Selected option %d for %s\n", optionIndex, sessionName)
					}
				}
				continue
			}

			msg := update.Message

			// Only accept from authorized user
			if msg.From.ID != config.ChatID {
				continue
			}

			chatID := msg.Chat.ID
			threadID := msg.MessageThreadID
			isGroup := msg.Chat.Type == "supergroup"

			// Handle voice messages
			if msg.Voice != nil && isGroup && threadID > 0 {
				config, _ = loadConfig()
				sessionName := getSessionByTopic(config, threadID)
				if sessionName != "" {
					tmuxName := "claude-" + sessionName
					if tmuxSessionExists(tmuxName) {
						sendMessage(config, chatID, threadID, "🎤 Transcribing...")
						// Download and transcribe
						audioPath := filepath.Join(os.TempDir(), fmt.Sprintf("voice_%d.ogg", time.Now().UnixNano()))
						if err := downloadTelegramFile(config, msg.Voice.FileID, audioPath); err != nil {
							sendMessage(config, chatID, threadID, fmt.Sprintf("❌ Download failed: %v", err))
						} else {
							transcription, err := transcribeAudio(config, audioPath)
							os.Remove(audioPath)
							if err != nil {
								sendMessage(config, chatID, threadID, fmt.Sprintf("❌ Transcription failed: %v", err))
							} else if transcription != "" {
								fmt.Printf("[voice] @%s: %s\n", msg.From.Username, transcription)
								sendMessage(config, chatID, threadID, fmt.Sprintf("📝 %s", transcription))
								sendToTmux(tmuxName, transcription)
							}
						}
					}
				}
				continue
			}

			// Handle photo messages
			if len(msg.Photo) > 0 && isGroup && threadID > 0 {
				config, _ = loadConfig()
				sessionName := getSessionByTopic(config, threadID)
				if sessionName != "" {
					tmuxName := "claude-" + sessionName
					if tmuxSessionExists(tmuxName) {
						// Get largest photo (last in array)
						photo := msg.Photo[len(msg.Photo)-1]
						imgPath := filepath.Join(os.TempDir(), fmt.Sprintf("telegram_%d.jpg", time.Now().UnixNano()))
						if err := downloadTelegramFile(config, photo.FileID, imgPath); err != nil {
							sendMessage(config, chatID, threadID, fmt.Sprintf("❌ Download failed: %v", err))
						} else {
							caption := msg.Caption
							if caption == "" {
								caption = "Analyze this image:"
							}
							prompt := fmt.Sprintf("%s %s", caption, imgPath)
							sendMessage(config, chatID, threadID, fmt.Sprintf("📷 Image saved, sending to Claude..."))
							// Send text first, wait for image to load, then send Enter
							sendToTmuxWithDelay(tmuxName, prompt, 2*time.Second)
						}
					}
				}
				continue
			}

			text := strings.TrimSpace(msg.Text)
			if text == "" {
				continue
			}

			// Strip bot mention from commands (e.g., /ping@botname -> /ping)
			if strings.HasPrefix(text, "/") {
				if idx := strings.Index(text, "@"); idx != -1 {
					spaceIdx := strings.Index(text, " ")
					if spaceIdx == -1 || idx < spaceIdx {
						text = text[:idx] + text[strings.Index(text+" ", " "):]
					}
				}
				text = strings.TrimSpace(text)
			}

			fmt.Printf("[%s] @%s: %s\n", msg.Chat.Type, msg.From.Username, text)

			// Handle commands
			if text == "/ping" {
				sendMessage(config, chatID, threadID, "pong!")
				continue
			}

			if text == "/away" {
				config.Away = !config.Away
				saveConfig(config)
				if config.Away {
					sendMessage(config, chatID, threadID, "🚶 Away mode ON")
				} else {
					sendMessage(config, chatID, threadID, "🏠 Away mode OFF")
				}
				continue
			}

			if text == "/list" {
				sessions, _ := listTmuxSessions()
				if len(sessions) == 0 {
					sendMessage(config, chatID, threadID, "No active sessions")
				} else {
					sendMessage(config, chatID, threadID, "Sessions:\n• "+strings.Join(sessions, "\n• "))
				}
				continue
			}

			if strings.HasPrefix(text, "/setdir") {
				arg := strings.TrimSpace(strings.TrimPrefix(text, "/setdir"))
				if arg == "" {
					currentDir := getProjectsDir(config)
					sendMessage(config, chatID, threadID, fmt.Sprintf("📁 Projects directory: %s\n\nUsage: /setdir ~/Projects", currentDir))
				} else {
					config.ProjectsDir = arg
					saveConfig(config)
					resolvedPath := getProjectsDir(config)
					sendMessage(config, chatID, threadID, fmt.Sprintf("✅ Projects directory set to: %s", resolvedPath))
				}
				continue
			}

			if strings.HasPrefix(text, "/kill ") {
				name := strings.TrimPrefix(text, "/kill ")
				name = strings.TrimSpace(name)
				if err := killSession(config, name); err != nil {
					sendMessage(config, chatID, threadID, fmt.Sprintf("❌ %v", err))
				} else {
					sendMessage(config, chatID, threadID, fmt.Sprintf("🗑️ Session '%s' killed", name))
					config, _ = loadConfig()
				}
				continue
			}

			if strings.HasPrefix(text, "/c ") {
				cmdStr := strings.TrimPrefix(text, "/c ")
				output, err := executeCommand(cmdStr)
				if err != nil {
					output = fmt.Sprintf("⚠️ %s\n\nExit: %v", output, err)
				}
				sendMessage(config, chatID, threadID, output)
				continue
			}

			// /new and /continue commands - create/restart session
			isNewCmd := strings.HasPrefix(text, "/new")
			isContinueCmd := strings.HasPrefix(text, "/continue")
			if (isNewCmd || isContinueCmd) && isGroup {
				config, _ = loadConfig()
				continueSession := isContinueCmd
				var arg string
				if isNewCmd {
					arg = strings.TrimSpace(strings.TrimPrefix(text, "/new"))
				} else {
					arg = strings.TrimSpace(strings.TrimPrefix(text, "/continue"))
				}
				cmdName := "/new"
				if continueSession {
					cmdName = "/continue"
				}

				// /new <name> or /continue <name> - create brand new session + topic
				if arg != "" {
					// Check if session already exists
					if _, exists := config.Sessions[arg]; exists {
						sendMessage(config, chatID, threadID, fmt.Sprintf("⚠️ Session '%s' already exists. Use %s without args in that topic to restart.", arg, cmdName))
						continue
					}
					// Create Telegram topic
					topicID, err := createForumTopic(config, arg)
					if err != nil {
						sendMessage(config, chatID, threadID, fmt.Sprintf("❌ Failed to create topic: %v", err))
						continue
					}
					// Resolve and create work directory
					workDir := resolveProjectPath(config, arg)
					// Save mapping with full path
					config.Sessions[arg] = &SessionInfo{
						TopicID: topicID,
						Path:    workDir,
					}
					saveConfig(config)
					if _, err := os.Stat(workDir); os.IsNotExist(err) {
						os.MkdirAll(workDir, 0755)
					}
					// Create tmux session
					tmuxName := "claude-" + arg
					if err := createTmuxSession(tmuxName, workDir, continueSession); err != nil {
						sendMessage(config, config.GroupID, topicID, fmt.Sprintf("❌ Failed to start tmux: %v", err))
					} else {
						// Verify session is actually running
						time.Sleep(500 * time.Millisecond)
						if tmuxSessionExists(tmuxName) {
							sendMessage(config, config.GroupID, topicID, fmt.Sprintf("🚀 Session '%s' started!\n\nSend messages here to interact with Claude.", arg))
						} else {
							sendMessage(config, config.GroupID, topicID, fmt.Sprintf("⚠️ Session '%s' created but died immediately. Check if ~/bin/ccc works.", arg))
						}
					}
					continue
				}

				// Without args - restart session in current topic
				if threadID > 0 {
					sessionName := getSessionByTopic(config, threadID)
					if sessionName == "" {
						sendMessage(config, chatID, threadID, fmt.Sprintf("❌ No session mapped to this topic. Use %s <name> to create one.", cmdName))
						continue
					}
					tmuxName := "claude-" + sessionName
					// Kill existing session if running
					if tmuxSessionExists(tmuxName) {
						killTmuxSession(tmuxName)
						time.Sleep(300 * time.Millisecond)
					}
					// Resolve and create work directory
					workDir := resolveProjectPath(config, sessionName)
					if _, err := os.Stat(workDir); os.IsNotExist(err) {
						os.MkdirAll(workDir, 0755)
					}
					if err := createTmuxSession(tmuxName, workDir, continueSession); err != nil {
						sendMessage(config, chatID, threadID, fmt.Sprintf("❌ Failed to start: %v", err))
					} else {
						time.Sleep(500 * time.Millisecond)
						if tmuxSessionExists(tmuxName) {
							action := "restarted"
							if continueSession {
								action = "continued"
							}
							sendMessage(config, chatID, threadID, fmt.Sprintf("🚀 Session '%s' %s", sessionName, action))
						} else {
							sendMessage(config, chatID, threadID, fmt.Sprintf("⚠️ Session died immediately"))
						}
					}
				} else {
					sendMessage(config, chatID, threadID, fmt.Sprintf("Usage: %s <name> to create a new session", cmdName))
				}
				continue
			}

			// Check if message is in a topic (interactive session)
			if isGroup && threadID > 0 {
				// Reload config to get latest sessions
				config, _ = loadConfig()
				sessionName := getSessionByTopic(config, threadID)
				if sessionName != "" {
					// Send to tmux session
					tmuxName := "claude-" + sessionName
					if tmuxSessionExists(tmuxName) {
						if err := sendToTmux(tmuxName, text); err != nil {
							sendMessage(config, chatID, threadID, fmt.Sprintf("❌ Failed to send: %v", err))
						}
					} else {
						sendMessage(config, chatID, threadID, "⚠️ Session not running. Use /new or /continue to restart.")
					}
					continue
				}
			}

			// Private chat: run one-shot Claude
			if !isGroup {
				sendMessage(config, chatID, threadID, "🤖 Running Claude...")

				prompt := text
				if msg.ReplyToMessage != nil && msg.ReplyToMessage.Text != "" {
					origText := msg.ReplyToMessage.Text
					origWords := strings.Fields(origText)
					if len(origWords) > 0 {
						home, _ := os.UserHomeDir()
						potentialDir := filepath.Join(home, origWords[0])
						if info, err := os.Stat(potentialDir); err == nil && info.IsDir() {
							prompt = origWords[0] + " " + text
						}
					}
					prompt = fmt.Sprintf("Original message:\n%s\n\nReply:\n%s", origText, prompt)
				}

				go func(p string, cid int64) {
					defer func() {
						if r := recover(); r != nil {
							sendMessage(config, cid, 0, fmt.Sprintf("💥 Panic: %v", r))
						}
					}()
					output, err := runClaude(p)
					if err != nil {
						if strings.Contains(err.Error(), "context deadline exceeded") {
							output = fmt.Sprintf("⏱️ Timeout (10min)\n\n%s", output)
						} else {
							output = fmt.Sprintf("⚠️ %s\n\nExit: %v", output, err)
						}
					}
					sendMessage(config, cid, 0, output)
				}(prompt, chatID)
			}
		}
	}
}

func printHelp() {
	fmt.Printf(`ccc - Claude Code Companion v%s

Your companion for Claude Code - control sessions remotely via Telegram and tmux.

USAGE:
    ccc                     Start/attach tmux session in current directory
    ccc -c                  Continue previous session
    ccc <message>           Send notification (if away mode is on)

COMMANDS:
    setup <token>           Complete setup (bot, hook, service - all in one!)
    doctor                  Check all dependencies and configuration
    config                  Show/set configuration values
    config projects-dir <path>  Set base directory for projects
    setgroup                Configure Telegram group for topics (if skipped during setup)
    listen                  Start the Telegram bot listener manually
    install                 Install Claude hook manually
    send <file>             Send file to current session's Telegram topic
    relay [port]            Start relay server for large files (default: 8080)
    run                     Run Claude directly (used by tmux sessions)
    hook                    Handle Claude hook (internal)

TELEGRAM COMMANDS:
    /ping                   Check if bot is alive
    /away                   Toggle away mode
    /new <name>             Create new session with topic (in projects_dir)
    /new ~/path/name        Create session with custom path
    /new                    Restart session in current topic (kills if running)
    /continue <name>        Create new session with -c flag
    /continue               Restart session with -c flag (kills if running)
    /kill <name>            Kill a session
    /list                   List active sessions
    /setdir <path>          Set base directory for projects
    /c <cmd>                Execute shell command

FLAGS:
    -h, --help              Show this help
    -v, --version           Show version

For more info: https://github.com/kidandcat/ccc
`, version)
}
