package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxResponseSize = 10 * 1024 * 1024 // 10MB

// redactTokenError replaces the bot token in error messages with "***"
func redactTokenError(err error, token string) error {
	if err == nil || token == "" {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), token, "***"))
}

// telegramGet performs an HTTP GET and redacts the bot token from any errors
func telegramGet(token string, url string) (*http.Response, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, redactTokenError(err, token)
	}
	return resp, nil
}

// telegramClientGet performs an HTTP GET with a custom client and redacts the bot token from any errors
func telegramClientGet(client *http.Client, token string, url string) (*http.Response, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, redactTokenError(err, token)
	}
	return resp, nil
}

// updateCCC downloads the latest ccc binary and restarts by exiting (service manager restarts)
func updateCCC(config *Config, chatID, threadID int64) {
	sendMessage(config, chatID, threadID, "🔄 Updating ccc...")

	// go install puts binary in ~/go/bin/ccc
	output, err := executeCommand("go install github.com/kidandcat/ccc@latest")
	if err != nil {
		sendMessage(config, chatID, threadID, fmt.Sprintf("❌ go install failed:\n%s", output))
		return
	}

	// If current binary is not in ~/go/bin, copy the new one over it
	home, _ := os.UserHomeDir()
	gobin := filepath.Join(home, "go", "bin", "ccc")
	if cccPath != gobin {
		if _, err := os.Stat(gobin); err == nil {
			data, err := os.ReadFile(gobin)
			if err != nil {
				sendMessage(config, chatID, threadID, fmt.Sprintf("❌ Failed to read new binary: %v", err))
				return
			}
			if err := os.WriteFile(cccPath, data, 0755); err != nil {
				sendMessage(config, chatID, threadID, fmt.Sprintf("❌ Failed to write binary to %s: %v", cccPath, err))
				return
			}
		}
	}

	sendMessage(config, chatID, threadID, "✅ Updated. Restarting...")
	os.Exit(0)
}

func telegramAPI(config *Config, method string, params url.Values) (*TelegramResponse, error) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/%s", config.BotToken, method)
	resp, err := http.PostForm(apiURL, params)
	if err != nil {
		return nil, redactTokenError(err, config.BotToken)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	var result TelegramResponse
	json.Unmarshal(body, &result)
	return &result, nil
}

func sendMessage(config *Config, chatID int64, threadID int64, text string) error {
	_, err := sendMessageGetID(config, chatID, threadID, text)
	return err
}

// sendMessageGetID sends a message and returns the message ID for later editing
func sendMessageGetID(config *Config, chatID int64, threadID int64, text string) (int64, error) {
	const maxLen = 4000

	// Split long messages
	messages := splitMessage(text, maxLen)
	var lastMsgID int64

	for _, msg := range messages {
		params := url.Values{
			"chat_id": {fmt.Sprintf("%d", chatID)},
			"text":    {msg},
		}
		if threadID > 0 {
			params.Set("message_thread_id", fmt.Sprintf("%d", threadID))
		}

		result, err := telegramAPI(config, "sendMessage", params)
		if err != nil {
			return 0, err
		}
		if !result.OK {
			return 0, fmt.Errorf("telegram error: %s", result.Description)
		}

		// Extract message_id from result
		if len(result.Result) > 0 {
			var msgResult struct {
				MessageID int64 `json:"message_id"`
			}
			if json.Unmarshal(result.Result, &msgResult) == nil {
				lastMsgID = msgResult.MessageID
			}
		}

		// Small delay between messages to maintain order
		if len(messages) > 1 {
			time.Sleep(100 * time.Millisecond)
		}
	}
	return lastMsgID, nil
}

// editMessage edits an existing message, sending overflow as new messages
func editMessage(config *Config, chatID int64, messageID int64, threadID int64, text string) error {
	const maxLen = 4000

	// Split message - first part goes to edit, rest as new messages
	messages := splitMessage(text, maxLen)

	// Edit existing message with first part
	params := url.Values{
		"chat_id":    {fmt.Sprintf("%d", chatID)},
		"message_id": {fmt.Sprintf("%d", messageID)},
		"text":       {messages[0]},
	}

	result, err := telegramAPI(config, "editMessageText", params)
	if err != nil {
		return err
	}
	if !result.OK {
		// If edit fails (e.g., message not modified), ignore
		return nil
	}

	// Send remaining parts as new messages
	for i := 1; i < len(messages); i++ {
		time.Sleep(100 * time.Millisecond)
		sendMessage(config, chatID, threadID, messages[i])
	}

	return nil
}

func sendMessageWithKeyboard(config *Config, chatID int64, threadID int64, text string, buttons [][]InlineKeyboardButton) error {
	const maxLen = 4000

	// Split long messages - send all but last as regular messages, last with keyboard
	messages := splitMessage(text, maxLen)

	// Send all but the last message as regular messages
	for i := 0; i < len(messages)-1; i++ {
		sendMessage(config, chatID, threadID, messages[i])
		time.Sleep(100 * time.Millisecond)
	}

	// Send the last message with keyboard
	keyboard := map[string]interface{}{
		"inline_keyboard": buttons,
	}
	keyboardJSON, _ := json.Marshal(keyboard)

	params := url.Values{
		"chat_id":      {fmt.Sprintf("%d", chatID)},
		"text":         {messages[len(messages)-1]},
		"reply_markup": {string(keyboardJSON)},
	}
	if threadID > 0 {
		params.Set("message_thread_id", fmt.Sprintf("%d", threadID))
	}

	result, err := telegramAPI(config, "sendMessage", params)
	if err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("telegram error: %s", result.Description)
	}
	return nil
}

func answerCallbackQuery(config *Config, callbackID string) {
	params := url.Values{
		"callback_query_id": {callbackID},
	}
	telegramAPI(config, "answerCallbackQuery", params)
}

func editMessageRemoveKeyboard(config *Config, chatID int64, messageID int, newText string) {
	const maxLen = 4000
	if len(newText) > maxLen {
		newText = newText[:maxLen-3] + "..."
	}

	params := url.Values{
		"chat_id":    {fmt.Sprintf("%d", chatID)},
		"message_id": {fmt.Sprintf("%d", messageID)},
		"text":       {newText},
	}
	telegramAPI(config, "editMessageText", params)
}

func sendTypingAction(config *Config, chatID int64, threadID int64) {
	params := url.Values{
		"chat_id": {fmt.Sprintf("%d", chatID)},
		"action":  {"typing"},
	}
	if threadID > 0 {
		params.Set("message_thread_id", fmt.Sprintf("%d", threadID))
	}
	telegramAPI(config, "sendChatAction", params)
}

func splitMessage(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}

	var messages []string
	remaining := text

	for len(remaining) > 0 {
		if len(remaining) <= maxLen {
			messages = append(messages, remaining)
			break
		}

		// Find a good split point (newline or space)
		splitAt := maxLen

		// Try to split at a newline first
		if idx := strings.LastIndex(remaining[:maxLen], "\n"); idx > maxLen/2 {
			splitAt = idx + 1
		} else if idx := strings.LastIndex(remaining[:maxLen], " "); idx > maxLen/2 {
			// Fall back to space
			splitAt = idx + 1
		}

		messages = append(messages, strings.TrimRight(remaining[:splitAt], " \n"))
		remaining = remaining[splitAt:]
	}

	return messages
}

// sendFile sends a file to Telegram (max 50MB)
func sendFile(config *Config, chatID int64, threadID int64, filePath string, caption string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add chat_id
	writer.WriteField("chat_id", fmt.Sprintf("%d", chatID))
	if threadID > 0 {
		writer.WriteField("message_thread_id", fmt.Sprintf("%d", threadID))
	}
	if caption != "" {
		writer.WriteField("caption", caption)
	}

	// Add file
	part, err := writer.CreateFormFile("document", filepath.Base(filePath))
	if err != nil {
		return err
	}
	io.Copy(part, file)
	writer.Close()

	resp, err := http.Post(
		fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument", config.BotToken),
		writer.FormDataContentType(),
		body,
	)
	if err != nil {
		return redactTokenError(err, config.BotToken)
	}
	defer resp.Body.Close()

	var result TelegramResponse
	json.NewDecoder(resp.Body).Decode(&result)
	if !result.OK {
		return fmt.Errorf("telegram error: %s", result.Description)
	}
	return nil
}

// downloadTelegramFile downloads a file from Telegram
func downloadTelegramFile(config *Config, fileID string, destPath string) error {
	// Get file path from Telegram
	resp, err := telegramGet(config.BotToken, fmt.Sprintf("https://api.telegram.org/bot%s/getFile?file_id=%s", config.BotToken, fileID))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var result struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("failed to get file path")
	}

	// Download the file
	fileURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", config.BotToken, result.Result.FilePath)
	fileResp, err := telegramGet(config.BotToken, fileURL)
	if err != nil {
		return err
	}
	defer fileResp.Body.Close()

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, fileResp.Body)
	return err
}

func createForumTopic(config *Config, name string) (int64, error) {
	if config.GroupID == 0 {
		return 0, fmt.Errorf("no group configured. Add bot to a group with topics enabled and run: ccc setgroup")
	}

	params := url.Values{
		"chat_id": {fmt.Sprintf("%d", config.GroupID)},
		"name":    {name},
	}

	result, err := telegramAPI(config, "createForumTopic", params)
	if err != nil {
		return 0, err
	}
	if !result.OK {
		return 0, fmt.Errorf("failed to create topic: %s", result.Description)
	}

	var topic TopicResult
	if err := json.Unmarshal(result.Result, &topic); err != nil {
		return 0, fmt.Errorf("failed to parse topic result: %w", err)
	}

	return topic.MessageThreadID, nil
}

// setBotCommands sets the bot commands in Telegram
func setBotCommands(botToken string) {
	commands := `{
		"commands": [
			{"command": "new", "description": "Create/restart session: /new <name>"},
			{"command": "c", "description": "Execute shell command: /c <cmd>"},
			{"command": "update", "description": "Update ccc binary from GitHub"}
		]
	}`

	resp, err := http.Post(
		fmt.Sprintf("https://api.telegram.org/bot%s/setMyCommands", botToken),
		"application/json",
		strings.NewReader(commands),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to set bot commands: %v\n", redactTokenError(err, botToken))
		return
	}
	resp.Body.Close()
}
