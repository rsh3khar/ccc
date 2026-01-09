package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func handleHook() error {
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hook: no config\n")
		return nil
	}

	// Read hook data from stdin
	var hookData HookData
	decoder := json.NewDecoder(os.Stdin)
	if err := decoder.Decode(&hookData); err != nil {
		fmt.Fprintf(os.Stderr, "hook: decode error: %v\n", err)
		return nil
	}

	fmt.Fprintf(os.Stderr, "hook: cwd=%s transcript=%s\n", hookData.Cwd, hookData.TranscriptPath)

	// Find session by matching cwd with saved path
	var sessionName string
	var topicID int64
	for name, info := range config.Sessions {
		if info == nil {
			continue
		}
		// Match against saved path or suffix
		if hookData.Cwd == info.Path || strings.HasSuffix(hookData.Cwd, "/"+name) {
			sessionName = name
			topicID = info.TopicID
			break
		}
	}
	if sessionName == "" || config.GroupID == 0 {
		fmt.Fprintf(os.Stderr, "hook: no session found for cwd=%s\n", hookData.Cwd)
		return nil
	}

	fmt.Fprintf(os.Stderr, "hook: session=%s topic=%d\n", sessionName, topicID)

	// Read last message from transcript
	lastMessage := "Session ended"
	if hookData.TranscriptPath != "" {
		if msg := getLastAssistantMessage(hookData.TranscriptPath); msg != "" {
			lastMessage = msg
		}
	}

	// Clear the cache so future PostToolUse hooks don't think this message was sent
	cacheFile := filepath.Join(os.TempDir(), "ccc-cache-"+sessionName)
	os.Remove(cacheFile)
	msgIDFile := filepath.Join(os.TempDir(), "ccc-msgid-"+sessionName)
	os.Remove(msgIDFile)

	// Always send the Stop message (final result)
	return sendMessage(config, config.GroupID, topicID, fmt.Sprintf("✅ %s\n\n%s", sessionName, lastMessage))
}

func handlePermissionHook() error {
	// Recover from any panic - hooks must never crash
	defer func() {
		recover()
	}()

	// Read stdin with timeout
	stdinData := make(chan []byte, 1)
	go func() {
		defer func() { recover() }()
		data, _ := io.ReadAll(os.Stdin)
		stdinData <- data
	}()

	var rawData []byte
	select {
	case rawData = <-stdinData:
	case <-time.After(2 * time.Second):
		return nil // Timeout, exit silently
	}

	if len(rawData) == 0 {
		return nil
	}

	// Parse JSON - ignore errors
	var hookData HookData
	if err := json.Unmarshal(rawData, &hookData); err != nil {
		return nil
	}

	// Load config - ignore errors
	config, err := loadConfig()
	if err != nil || config == nil {
		return nil
	}

	// Find session by matching cwd suffix
	var sessionName string
	var topicID int64
	for name, info := range config.Sessions {
		if name == "" || info == nil {
			continue
		}
		if hookData.Cwd == info.Path || strings.HasSuffix(hookData.Cwd, "/"+name) {
			sessionName = name
			topicID = info.TopicID
			break
		}
	}

	if sessionName == "" || config.GroupID == 0 {
		return nil
	}

	// Handle AskUserQuestion (plan approval, etc.) - in goroutine to not block
	fmt.Fprintf(os.Stderr, "hook-permission: tool=%s questions=%d\n", hookData.ToolName, len(hookData.ToolInput.Questions))
	if hookData.ToolName == "AskUserQuestion" && len(hookData.ToolInput.Questions) > 0 {
		go func() {
			defer func() { recover() }()
			for qIdx, q := range hookData.ToolInput.Questions {
				if q.Question == "" {
					continue
				}
				// Build message
				msg := fmt.Sprintf("❓ %s\n\n%s", q.Header, q.Question)

				// Build inline keyboard buttons
				var buttons [][]InlineKeyboardButton
				for i, opt := range q.Options {
					if opt.Label == "" {
						continue
					}
					// Callback data format: session:questionIndex:optionIndex
					// Telegram limits callback_data to 64 bytes
					callbackData := fmt.Sprintf("%s:%d:%d", sessionName, qIdx, i)
					if len(callbackData) > 64 {
						callbackData = callbackData[:64]
					}
					buttons = append(buttons, []InlineKeyboardButton{
						{Text: opt.Label, CallbackData: callbackData},
					})
				}

				if len(buttons) > 0 {
					sendMessageWithKeyboard(config, config.GroupID, topicID, msg, buttons)
				}
			}
		}()
		return nil
	}

	// Generic permission request - in goroutine to not block
	go func() {
		defer func() { recover() }()
		if hookData.ToolName != "" {
			msg := fmt.Sprintf("🔐 Permission requested: %s", hookData.ToolName)
			sendMessage(config, config.GroupID, topicID, msg)
		}
	}()

	return nil
}

func getLastAssistantMessage(transcriptPath string) string {
	file, err := os.Open(transcriptPath)
	if err != nil {
		return ""
	}
	defer file.Close()

	var lastMessage string
	scanner := bufio.NewScanner(file)
	// Increase buffer size for large lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024) // 10MB to handle large tool outputs

	for scanner.Scan() {
		var entry map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry["type"] == "assistant" {
			if msg, ok := entry["message"].(map[string]interface{}); ok {
				if content, ok := msg["content"].([]interface{}); ok {
					for _, c := range content {
						if block, ok := c.(map[string]interface{}); ok {
							if block["type"] == "text" {
								if text, ok := block["text"].(string); ok {
									lastMessage = text
								}
							}
						}
					}
				}
			}
		}
	}
	return lastMessage
}

func handlePromptHook() error {
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hook-prompt: no config\n")
		return nil
	}

	var hookData HookData
	decoder := json.NewDecoder(os.Stdin)
	if err := decoder.Decode(&hookData); err != nil {
		fmt.Fprintf(os.Stderr, "hook-prompt: decode error: %v\n", err)
		return nil
	}

	if hookData.Prompt == "" {
		fmt.Fprintf(os.Stderr, "hook-prompt: empty prompt\n")
		return nil
	}

	// Find session by matching cwd suffix
	var sessionName string
	var topicID int64
	for name, info := range config.Sessions {
		if info == nil {
			continue
		}
		if hookData.Cwd == info.Path || strings.HasSuffix(hookData.Cwd, "/"+name) {
			sessionName = name
			topicID = info.TopicID
			break
		}
	}

	if topicID == 0 || config.GroupID == 0 {
		fmt.Fprintf(os.Stderr, "hook-prompt: no topic found for cwd=%s\n", hookData.Cwd)
		return nil
	}

	// Cache the current last assistant message to prevent re-sending old messages
	if sessionName != "" && hookData.TranscriptPath != "" {
		if msg := getLastAssistantMessage(hookData.TranscriptPath); msg != "" {
			cacheFile := filepath.Join(os.TempDir(), "ccc-cache-"+sessionName)
			os.WriteFile(cacheFile, []byte(msg), 0600)
		}
	}

	// Send typing action
	sendTypingAction(config, config.GroupID, topicID)

	// Send the prompt to Telegram (sendMessage handles splitting long messages)
	fmt.Fprintf(os.Stderr, "hook-prompt: sending to topic %d\n", topicID)
	return sendMessage(config, config.GroupID, topicID, fmt.Sprintf("💬 %s", hookData.Prompt))
}

func handleOutputHook() error {
	config, err := loadConfig()
	if err != nil {
		return nil
	}

	rawData, _ := io.ReadAll(os.Stdin)
	if len(rawData) == 0 {
		return nil
	}

	var hookData HookData
	if err := json.Unmarshal(rawData, &hookData); err != nil {
		return nil
	}

	// Find session
	var sessionName string
	var topicID int64
	for name, info := range config.Sessions {
		if info == nil {
			continue
		}
		if hookData.Cwd == info.Path || strings.HasSuffix(hookData.Cwd, "/"+name) {
			sessionName = name
			topicID = info.TopicID
			break
		}
	}

	if topicID == 0 || config.GroupID == 0 || sessionName == "" {
		return nil
	}

	// Get last message from transcript
	if hookData.TranscriptPath != "" {
		if msg := getLastAssistantMessage(hookData.TranscriptPath); msg != "" {
			cacheFile := filepath.Join(os.TempDir(), "ccc-cache-"+sessionName)
			msgIDFile := filepath.Join(os.TempDir(), "ccc-msgid-"+sessionName)
			lastSent, _ := os.ReadFile(cacheFile)

			// PostToolUse: try to edit existing message
			if hookData.HookEventName == "PostToolUse" {
				if msgIDData, err := os.ReadFile(msgIDFile); err == nil {
					if msgID, err := strconv.ParseInt(string(msgIDData), 10, 64); err == nil && msgID > 0 {
						// Only edit if message changed
						if string(lastSent) != msg {
							os.WriteFile(cacheFile, []byte(msg), 0600)
							editMessage(config, config.GroupID, msgID, topicID, msg)
						}
						return nil
					}
				}
			}

			// PreToolUse or no existing message: check for duplicates, then send new
			if string(lastSent) == msg {
				return nil // Skip duplicate
			}
			os.WriteFile(cacheFile, []byte(msg), 0600)

			if msgID, err := sendMessageGetID(config, config.GroupID, topicID, msg); err == nil && msgID > 0 {
				os.WriteFile(msgIDFile, []byte(strconv.FormatInt(msgID, 10)), 0600)
			}
		}
	}

	return nil
}

func handleQuestionHook() error {
	config, err := loadConfig()
	if err != nil {
		return nil
	}

	rawData, _ := io.ReadAll(os.Stdin)
	if len(rawData) == 0 {
		return nil
	}

	var hookData HookData
	if err := json.Unmarshal(rawData, &hookData); err != nil {
		return nil
	}

	// Find session by matching cwd suffix
	var sessionName string
	var topicID int64
	for name, info := range config.Sessions {
		if info == nil {
			continue
		}
		if hookData.Cwd == info.Path || strings.HasSuffix(hookData.Cwd, "/"+name) {
			sessionName = name
			topicID = info.TopicID
			break
		}
	}

	if sessionName == "" || config.GroupID == 0 || topicID == 0 {
		return nil
	}

	// Send questions to Telegram
	for qIdx, q := range hookData.ToolInput.Questions {
		if q.Question == "" {
			continue
		}
		msg := fmt.Sprintf("❓ %s\n\n%s", q.Header, q.Question)

		var buttons [][]InlineKeyboardButton
		for i, opt := range q.Options {
			if opt.Label == "" {
				continue
			}
			callbackData := fmt.Sprintf("%s:%d:%d", sessionName, qIdx, i)
			if len(callbackData) > 64 {
				callbackData = callbackData[:64]
			}
			buttons = append(buttons, []InlineKeyboardButton{
				{Text: opt.Label, CallbackData: callbackData},
			})
		}

		if len(buttons) > 0 {
			sendMessageWithKeyboard(config, config.GroupID, topicID, msg, buttons)
		} else {
			sendMessage(config, config.GroupID, topicID, msg)
		}
	}

	return nil
}

func handleNotificationHook() error {
	config, err := loadConfig()
	if err != nil {
		return nil
	}

	rawData, _ := io.ReadAll(os.Stdin)
	if len(rawData) == 0 {
		return nil
	}

	var hookData HookData
	if err := json.Unmarshal(rawData, &hookData); err != nil {
		return nil
	}

	if hookData.Notification == "" {
		return nil
	}

	// Find session by matching cwd suffix
	var topicID int64
	for name, info := range config.Sessions {
		if info == nil {
			continue
		}
		if hookData.Cwd == info.Path || strings.HasSuffix(hookData.Cwd, "/"+name) {
			topicID = info.TopicID
			break
		}
	}

	if topicID == 0 || config.GroupID == 0 {
		return nil
	}

	return sendMessage(config, config.GroupID, topicID, fmt.Sprintf("🔔 %s", hookData.Notification))
}

// isCccHook checks if a hook entry contains a ccc command
func isCccHook(entry interface{}) bool {
	// Direct command hook: {"command": "...", "type": "command"}
	if m, ok := entry.(map[string]interface{}); ok {
		if cmd, ok := m["command"].(string); ok {
			return strings.Contains(cmd, "ccc hook")
		}
		// Wrapper hook: {"hooks": [...], "matcher": "..."}
		if hooks, ok := m["hooks"].([]interface{}); ok {
			for _, h := range hooks {
				if hm, ok := h.(map[string]interface{}); ok {
					if cmd, ok := hm["command"].(string); ok {
						if strings.Contains(cmd, "ccc hook") {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

// removeCccHooks removes all ccc hooks from a hook array
func removeCccHooks(hookArray []interface{}) []interface{} {
	var result []interface{}
	for _, entry := range hookArray {
		if !isCccHook(entry) {
			result = append(result, entry)
		}
	}
	return result
}

func installHook() error {
	home, _ := os.UserHomeDir()
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	cccPath := filepath.Join(home, "bin", "ccc")

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return fmt.Errorf("failed to read settings.json: %w", err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("failed to parse settings.json: %w", err)
	}

	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		hooks = make(map[string]interface{})
	}

	// Define all ccc hooks to install (new format with matcher and hooks array)
	cccHooks := map[string][]interface{}{
		"Stop": {
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"command": cccPath + " hook",
						"type":    "command",
					},
				},
				"matcher": "",
			},
		},
		"Notification": {
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"command": cccPath + " hook-notification",
						"type":    "command",
					},
				},
				"matcher": "",
			},
		},
		"PermissionRequest": {
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"command": cccPath + " hook-permission",
						"type":    "command",
					},
				},
				"matcher": "",
			},
		},
		"PostToolUse": {
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"command": cccPath + " hook-output",
						"type":    "command",
					},
				},
				"matcher": "",
			},
		},
		"PreToolUse": {
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"command": cccPath + " hook-question",
						"type":    "command",
					},
				},
				"matcher": "AskUserQuestion",
			},
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"command": cccPath + " hook-output",
						"type":    "command",
					},
				},
				"matcher": "",
			},
		},
		"UserPromptSubmit": {
			map[string]interface{}{
				"hooks": []interface{}{
					map[string]interface{}{
						"command": cccPath + " hook-prompt",
						"type":    "command",
					},
				},
				"matcher": "",
			},
		},
	}

	// For each hook type, remove existing ccc hooks and add new ones
	for hookType, newHooks := range cccHooks {
		var existingHooks []interface{}
		if existing, ok := hooks[hookType].([]interface{}); ok {
			existingHooks = removeCccHooks(existing)
		}
		// Add ccc hooks to the beginning
		hooks[hookType] = append(newHooks, existingHooks...)
	}

	settings["hooks"] = hooks

	newData, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	if err := os.WriteFile(settingsPath, newData, 0600); err != nil {
		return fmt.Errorf("failed to write settings.json: %w", err)
	}

	fmt.Println("✅ Claude hooks installed!")
	return nil
}

func uninstallHook() error {
	home, _ := os.UserHomeDir()
	settingsPath := filepath.Join(home, ".claude", "settings.json")

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return fmt.Errorf("failed to read settings.json: %w", err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("failed to parse settings.json: %w", err)
	}

	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		fmt.Println("No hooks found")
		return nil
	}

	// Remove ccc hooks from each hook type
	hookTypes := []string{"Stop", "Notification", "PermissionRequest", "PostToolUse", "PreToolUse", "UserPromptSubmit"}
	for _, hookType := range hookTypes {
		if existing, ok := hooks[hookType].([]interface{}); ok {
			filtered := removeCccHooks(existing)
			if len(filtered) == 0 {
				delete(hooks, hookType)
			} else {
				hooks[hookType] = filtered
			}
		}
	}

	settings["hooks"] = hooks

	newData, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}

	if err := os.WriteFile(settingsPath, newData, 0600); err != nil {
		return fmt.Errorf("failed to write settings.json: %w", err)
	}

	fmt.Println("✅ Claude hooks uninstalled!")
	return nil
}

func installSkill() error {
	home, _ := os.UserHomeDir()
	skillDir := filepath.Join(home, ".claude", "skills")
	skillPath := filepath.Join(skillDir, "ccc-send.md")

	if err := os.MkdirAll(skillDir, 0755); err != nil {
		return fmt.Errorf("failed to create skills directory: %w", err)
	}

	skillContent := `# CCC Send - File Transfer Skill

## Description
Send files to the user via Telegram using the ccc send command.

## Usage
When the user asks you to send them a file, or when you have generated/built a file that the user needs (like an APK, binary, or any other file), use this command:

` + "```bash" + `
ccc send <file_path>
` + "```" + `

## How it works
- **Small files (< 50MB)**: Sent directly via Telegram
- **Large files (≥ 50MB)**: Streamed via relay server with a one-time download link

## Examples

### Send a built APK
` + "```bash" + `
ccc send ./build/app.apk
` + "```" + `

### Send a generated file
` + "```bash" + `
ccc send ./output/report.pdf
` + "```" + `

### Send from subdirectory
` + "```bash" + `
ccc send ~/Downloads/large-file.zip
` + "```" + `

## Important Notes
- The command detects the current session from your working directory
- For large files, the command will wait up to 10 minutes for the user to download
- Each download link is one-time use only
- Use this proactively when you've created files the user needs!
`

	if err := os.WriteFile(skillPath, []byte(skillContent), 0644); err != nil {
		return fmt.Errorf("failed to write skill file: %w", err)
	}

	fmt.Println("✅ CCC send skill installed!")
	return nil
}

func uninstallSkill() error {
	home, _ := os.UserHomeDir()
	skillPath := filepath.Join(home, ".claude", "skills", "ccc-send.md")
	os.Remove(skillPath)
	return nil
}
