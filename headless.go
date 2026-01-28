package main

import (
	"bytes"
	"context"
	"crypto/rand"
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
	"sync"
	"syscall"
	"time"
)

var (
	busySessions sync.Map // session name -> bool
)

// runClaudeHeadless runs claude in non-interactive mode with session continuity
func runClaudeHeadless(config *Config, prompt string, sessionInfo *SessionInfo, workDir string) (string, error) {
	if claudePath == "" {
		return "", fmt.Errorf("claude binary not found")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	args := []string{"--dangerously-skip-permissions", "-p", prompt}

	if sessionInfo.ClaudeSessionID != "" {
		// Resume existing session
		args = append(args, "--resume", sessionInfo.ClaudeSessionID)
	} else {
		// Generate new session ID
		uuid, err := generateUUID()
		if err != nil {
			return "", fmt.Errorf("failed to generate session ID: %w", err)
		}
		sessionInfo.ClaudeSessionID = uuid
		args = append(args, "--session-id", uuid)
	}

	cmd := exec.CommandContext(ctx, claudePath, args...)
	cmd.Dir = workDir

	// Set environment - pass OAuth token
	cmd.Env = os.Environ()
	oauthToken := config.OAuthToken
	if oauthToken == "" {
		oauthToken = os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	}
	if oauthToken != "" {
		cmd.Env = append(cmd.Env, "CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)
	}

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

func generateUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// Set version 4 and variant bits
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// runHeadless starts the Telegram listener in headless mode (no tmux, uses claude -p)
func runHeadless() error {
	// Small random delay to avoid race conditions when multiple instances start
	time.Sleep(time.Duration(os.Getpid()%500) * time.Millisecond)

	// Use a lock file to ensure only one instance runs
	home, _ := os.UserHomeDir()
	lockPath := filepath.Join(home, ".ccc-headless.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("failed to open lock file: %w", err)
	}
	defer lockFile.Close()

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fmt.Println("Another ccc headless instance is already running, exiting quietly")
		os.Exit(0)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

	lockFile.Truncate(0)
	lockFile.Seek(0, 0)
	fmt.Fprintf(lockFile, "%d\n", os.Getpid())

	config, err := loadConfig()
	if err != nil {
		return fmt.Errorf("not configured. Run: ccc setup <bot_token>")
	}

	fmt.Printf("Headless bot listening... (chat: %d, group: %d)\n", config.ChatID, config.GroupID)
	fmt.Printf("Active sessions: %d\n", len(config.Sessions))
	if config.OAuthToken != "" {
		fmt.Println("OAuth token: configured")
	} else if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "" {
		fmt.Println("OAuth token: from environment")
	} else {
		fmt.Println("OAuth token: NOT SET (claude may fail to authenticate)")
	}
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
		resp, err := telegramClientGet(client, config.BotToken, reqURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Network error: %v (retrying...)\n", err)
			time.Sleep(5 * time.Second)
			continue
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
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

			// Handle callback queries (button presses) - not fully supported in headless
			if update.CallbackQuery != nil {
				cb := update.CallbackQuery
				if cb.From.ID != config.ChatID {
					continue
				}
				answerCallbackQuery(config, cb.ID)

				// Parse callback data and send as text to claude
				parts := strings.Split(cb.Data, ":")
				if len(parts) == 3 {
					sessName := parts[0]
					optionIndex, _ := strconv.Atoi(parts[2])

					if cb.Message != nil {
						originalText := cb.Message.Text
						newText := fmt.Sprintf("%s\n\n✓ Selected option %d", originalText, optionIndex+1)
						editMessageRemoveKeyboard(config, cb.Message.Chat.ID, cb.Message.MessageID, newText)
					}

					// In headless mode, send the option as text to the session
					sessionInfo := config.Sessions[sessName]
					if sessionInfo != nil {
						optionText := fmt.Sprintf("Option %d", optionIndex+1)
						// Try to get actual option label from message text
						if cb.Message != nil && cb.Message.Text != "" {
							optionText = fmt.Sprintf("I select option %d", optionIndex+1)
						}
						go func(si *SessionInfo, name, text string) {
							defer func() { recover() }()
							handleHeadlessPrompt(config, name, si, text)
						}(sessionInfo, sessName, optionText)
					}
				}
				continue
			}

			msg := update.Message

			if msg.From.ID != config.ChatID {
				continue
			}

			chatID := msg.Chat.ID
			threadID := msg.MessageThreadID
			isGroup := msg.Chat.Type == "supergroup"

			// Handle voice messages
			if msg.Voice != nil && isGroup && threadID > 0 {
				config, _ = loadConfig()
				sessName := getSessionByTopic(config, threadID)
				if sessName != "" {
					sessionInfo := config.Sessions[sessName]
					sendMessage(config, chatID, threadID, "🎤 Transcribing...")
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
							go func(si *SessionInfo, name, text string) {
								defer func() { recover() }()
								handleHeadlessPrompt(config, name, si, "[Audio transcription, may contain errors]: "+text)
							}(sessionInfo, sessName, transcription)
						}
					}
				}
				continue
			}

			// Handle photo messages
			if len(msg.Photo) > 0 && isGroup && threadID > 0 {
				config, _ = loadConfig()
				sessName := getSessionByTopic(config, threadID)
				if sessName != "" {
					sessionInfo := config.Sessions[sessName]
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
						sendMessage(config, chatID, threadID, "📷 Image saved, sending to Claude...")
						go func(si *SessionInfo, name, text string) {
							defer func() { recover() }()
							handleHeadlessPrompt(config, name, si, text)
						}(sessionInfo, sessName, prompt)
					}
				}
				continue
			}

			text := strings.TrimSpace(msg.Text)
			if text == "" {
				continue
			}

			// Strip bot mention
			if strings.HasPrefix(text, "/") {
				if idx := strings.Index(text, "@"); idx != -1 {
					spaceIdx := strings.Index(text, " ")
					if spaceIdx == -1 || idx < spaceIdx {
						text = text[:idx] + text[strings.Index(text+" ", " "):]
					}
				}
				text = strings.TrimSpace(text)
			}

			fmt.Printf("[headless][%s] @%s: %s\n", msg.Chat.Type, msg.From.Username, text)

			// Handle commands
			if text == "/ping" {
				sendMessage(config, chatID, threadID, "pong! (headless)")
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
				var sessionList []string
				for name, info := range config.Sessions {
					status := "idle"
					if _, busy := busySessions.Load(name); busy {
						status = "running"
					}
					resumed := ""
					if info.ClaudeSessionID != "" {
						resumed = " (resumable)"
					}
					sessionList = append(sessionList, fmt.Sprintf("%s [%s]%s", name, status, resumed))
				}
				if len(sessionList) == 0 {
					sendMessage(config, chatID, threadID, "No sessions")
				} else {
					sendMessage(config, chatID, threadID, "Sessions:\n• "+strings.Join(sessionList, "\n• "))
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
				name := strings.TrimSpace(strings.TrimPrefix(text, "/kill "))
				if _, exists := config.Sessions[name]; !exists {
					sendMessage(config, chatID, threadID, fmt.Sprintf("❌ Session '%s' not found", name))
				} else {
					delete(config.Sessions, name)
					saveConfig(config)
					sendMessage(config, chatID, threadID, fmt.Sprintf("🗑️ Session '%s' removed", name))
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

			if text == "/update" {
				sendMessage(config, chatID, threadID, "🔄 Updating ccc...")
				go func(cid, tid int64) {
					output, err := executeCommand("go install github.com/kidandcat/ccc@latest")
					if err != nil {
						sendMessage(config, cid, tid, fmt.Sprintf("❌ Update failed:\n%s", output))
					} else {
						sendMessage(config, cid, tid, fmt.Sprintf("✅ ccc updated\n%s\n\nRestart the service to apply.", output))
					}
				}(chatID, threadID)
				continue
			}

			// /new and /continue commands
			isNewCmd := strings.HasPrefix(text, "/new")
			isContinueCmd := strings.HasPrefix(text, "/continue")
			if (isNewCmd || isContinueCmd) && isGroup {
				config, _ = loadConfig()
				var arg string
				if isNewCmd {
					arg = strings.TrimSpace(strings.TrimPrefix(text, "/new"))
				} else {
					arg = strings.TrimSpace(strings.TrimPrefix(text, "/continue"))
				}
				cmdName := "/new"
				if isContinueCmd {
					cmdName = "/continue"
				}

				if arg != "" {
					// Create new session + topic
					if _, exists := config.Sessions[arg]; exists {
						sendMessage(config, chatID, threadID, fmt.Sprintf("⚠️ Session '%s' already exists. Use %s without args in that topic to restart.", arg, cmdName))
						continue
					}
					topicID, err := createForumTopic(config, arg)
					if err != nil {
						sendMessage(config, chatID, threadID, fmt.Sprintf("❌ Failed to create topic: %v", err))
						continue
					}
					workDir := resolveProjectPath(config, arg)
					if _, err := os.Stat(workDir); os.IsNotExist(err) {
						os.MkdirAll(workDir, 0755)
					}
					config.Sessions[arg] = &SessionInfo{
						TopicID: topicID,
						Path:    workDir,
					}
					saveConfig(config)
					sendMessage(config, config.GroupID, topicID, fmt.Sprintf("🚀 Session '%s' created (headless)\n\nSend messages here to interact with Claude.", arg))
					continue
				}

				// Without args - reset session in current topic
				if threadID > 0 {
					sessName := getSessionByTopic(config, threadID)
					if sessName == "" {
						sendMessage(config, chatID, threadID, fmt.Sprintf("❌ No session mapped to this topic. Use %s <name> to create one.", cmdName))
						continue
					}
					if isContinueCmd {
						// Keep session ID for continuity
						sendMessage(config, chatID, threadID, fmt.Sprintf("🚀 Session '%s' will continue from last conversation", sessName))
					} else {
						// Reset session ID
						config.Sessions[sessName].ClaudeSessionID = ""
						saveConfig(config)
						sendMessage(config, chatID, threadID, fmt.Sprintf("🚀 Session '%s' reset (new conversation)", sessName))
					}
				} else {
					sendMessage(config, chatID, threadID, fmt.Sprintf("Usage: %s <name> to create a new session", cmdName))
				}
				continue
			}

			// Message in a topic -> run headless Claude
			if isGroup && threadID > 0 {
				config, _ = loadConfig()
				sessName := getSessionByTopic(config, threadID)
				if sessName != "" {
					sessionInfo := config.Sessions[sessName]
					go func(si *SessionInfo, name, prompt string) {
						defer func() { recover() }()
						handleHeadlessPrompt(config, name, si, prompt)
					}(sessionInfo, sessName, text)
				} else {
					sendMessage(config, chatID, threadID, "⚠️ No session linked to this topic. Use /new <name> to create one.")
				}
				continue
			}

			// Private chat: one-shot Claude
			if !isGroup {
				sendMessage(config, chatID, threadID, "🤖 Running Claude (headless)...")

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

// headlessStart creates a session and sends the first prompt (used by CLI, not Telegram)
func headlessStart(name string, path string, prompt string) error {
	config, err := loadConfig()
	if err != nil {
		return fmt.Errorf("not configured. Run: ccc setup <bot_token>")
	}

	if config.GroupID == 0 {
		return fmt.Errorf("no group configured")
	}

	// Create or reuse session
	sessionInfo, exists := config.Sessions[name]
	if !exists {
		topicID, err := createForumTopic(config, name)
		if err != nil {
			return fmt.Errorf("failed to create topic: %w", err)
		}
		sessionInfo = &SessionInfo{
			TopicID: topicID,
			Path:    path,
		}
		config.Sessions[name] = sessionInfo
		saveConfig(config)
		fmt.Printf("Created session '%s' with topic\n", name)
	} else {
		// Update path if changed
		sessionInfo.Path = path
		saveConfig(config)
	}

	sendMessage(config, config.GroupID, sessionInfo.TopicID, fmt.Sprintf("🚀 Session '%s' started (headless-start)\n\n💬 %s", name, prompt))

	// Run the prompt synchronously
	fmt.Printf("Running prompt in session '%s'...\n", name)
	handleHeadlessPrompt(config, name, sessionInfo, prompt)
	fmt.Printf("Prompt completed for session '%s'. Continue via Telegram.\n", name)
	return nil
}

// handleHeadlessPrompt runs a prompt in headless mode for a session
func handleHeadlessPrompt(config *Config, sessName string, sessionInfo *SessionInfo, prompt string) {
	// Check if session is busy
	if _, busy := busySessions.LoadOrStore(sessName, true); busy {
		if config.GroupID != 0 && sessionInfo.TopicID != 0 {
			sendMessage(config, config.GroupID, sessionInfo.TopicID, "⏳ Session busy, wait for current task to finish...")
		}
		return
	}
	defer busySessions.Delete(sessName)

	workDir := sessionInfo.Path
	if workDir == "" {
		workDir = resolveProjectPath(config, sessName)
	}
	if _, err := os.Stat(workDir); os.IsNotExist(err) {
		os.MkdirAll(workDir, 0755)
	}

	// Send typing indicator
	if config.GroupID != 0 && sessionInfo.TopicID != 0 {
		sendTypingAction(config, config.GroupID, sessionInfo.TopicID)
	}

	hadSessionID := sessionInfo.ClaudeSessionID != ""

	fmt.Printf("[headless] Running claude for session '%s' (resume=%s)\n", sessName, sessionInfo.ClaudeSessionID)

	output, err := runClaudeHeadless(config, prompt, sessionInfo, workDir)

	// Save session ID if it was just generated
	if !hadSessionID && sessionInfo.ClaudeSessionID != "" {
		// Reload config to avoid overwriting concurrent changes
		freshConfig, loadErr := loadConfig()
		if loadErr == nil {
			if si, exists := freshConfig.Sessions[sessName]; exists {
				si.ClaudeSessionID = sessionInfo.ClaudeSessionID
				saveConfig(freshConfig)
			}
		}
	}

	if err != nil {
		if strings.Contains(err.Error(), "context deadline exceeded") {
			output = fmt.Sprintf("⏱️ Timeout (10min)\n\n%s", output)
		} else if output == "" {
			output = fmt.Sprintf("❌ Error: %v", err)
		}
	}

	// Send output to Telegram (hooks may have already sent intermediate output)
	if config.GroupID != 0 && sessionInfo.TopicID != 0 {
		sendMessage(config, config.GroupID, sessionInfo.TopicID, fmt.Sprintf("✅ Done\n\n%s", output))
	}
}
