package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// RouterIntent represents the classified intent from the LLM router
type RouterIntent struct {
	Action  string // new_session, send, switch, status, peek, kill, passthrough, list
	Name    string // session name (for new_session, switch, peek, kill)
	Message string // message content (for new_session prompt, send message)
}

const routerSystemPrompt = `You are a command router for a Claude Code session manager. Classify the user's message into one of these intents:

INTENTS:
- new_session:<name>:<prompt> — User wants to create a new session. Extract a short kebab-case name and the task prompt.
- send:<message> — User wants to send a message to the active session. Extract the message.
- switch:<name> — User wants to switch to a different session.
- status — User wants to see all sessions and their status.
- peek:<name> — User wants to see the latest output from a specific session.
- kill:<name> — User wants to stop/kill a session.
- list — User wants to list all sessions.
- passthrough — The message should be forwarded as-is to the active session (default for most messages).

RULES:
1. If the message is clearly a task/question/instruction with no session management intent, classify as "passthrough".
2. For "new_session", generate a short descriptive name (2-3 words, kebab-case) from the task.
3. If the user says "start", "begin", "create", "new session", "new task" → new_session.
4. If the user says "what's happening", "status", "how are things", "progress" → status.
5. If the user says "show me", "peek", "check on", "look at" + session name → peek.
6. If the user says "stop", "kill", "end", "cancel" + session name → kill.
7. If the user says "switch to", "go to", "open" + session name → switch.
8. If the user says "list sessions", "show sessions", "what sessions" → list.
9. Most messages that look like instructions, questions, or code should be "passthrough".

Respond with ONLY the intent string, nothing else. Examples:
- "start a new session to research quantum computing" → new_session:quantum-research:research quantum computing and summarize key findings
- "what's the status" → status
- "check on the research session" → peek:research
- "stop the quantum session" → kill:quantum-research
- "switch to my-project" → switch:my-project
- "implement the login form with React" → passthrough
- "list all sessions" → list
- "hey can you fix the bug in auth.go" → passthrough`

const defaultRouterModel = "google/gemini-2.0-flash-lite-001"

// classifyIntent sends the message to OpenRouter for intent classification
func classifyIntent(config *Config, text string) (*RouterIntent, error) {
	if config.OpenRouterKey == "" {
		// No router key configured — treat everything as passthrough
		return &RouterIntent{Action: "passthrough", Message: text}, nil
	}

	reqBody := map[string]interface{}{
		"model": defaultRouterModel,
		"messages": []map[string]string{
			{"role": "system", "content": routerSystemPrompt},
			{"role": "user", "content": text},
		},
		"max_tokens":  100,
		"temperature": 0.0,
	}

	bodyJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", "https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(bodyJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+config.OpenRouterKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("router API call failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("router API error %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if len(result.Choices) == 0 {
		return &RouterIntent{Action: "passthrough", Message: text}, nil
	}

	return parseIntent(result.Choices[0].Message.Content, text)
}

// parseIntent parses the LLM response into a RouterIntent
func parseIntent(response string, originalText string) (*RouterIntent, error) {
	response = strings.TrimSpace(response)

	// Handle new_session:<name>:<prompt>
	if strings.HasPrefix(response, "new_session:") {
		parts := strings.SplitN(response, ":", 3)
		name := ""
		prompt := originalText
		if len(parts) >= 2 {
			name = strings.TrimSpace(parts[1])
		}
		if len(parts) >= 3 {
			prompt = strings.TrimSpace(parts[2])
		}
		if name == "" {
			name = "session"
		}
		return &RouterIntent{Action: "new_session", Name: name, Message: prompt}, nil
	}

	// Handle send:<message>
	if strings.HasPrefix(response, "send:") {
		msg := strings.TrimPrefix(response, "send:")
		return &RouterIntent{Action: "send", Message: strings.TrimSpace(msg)}, nil
	}

	// Handle switch:<name>
	if strings.HasPrefix(response, "switch:") {
		name := strings.TrimPrefix(response, "switch:")
		return &RouterIntent{Action: "switch", Name: strings.TrimSpace(name)}, nil
	}

	// Handle peek:<name>
	if strings.HasPrefix(response, "peek:") {
		name := strings.TrimPrefix(response, "peek:")
		return &RouterIntent{Action: "peek", Name: strings.TrimSpace(name)}, nil
	}

	// Handle kill:<name>
	if strings.HasPrefix(response, "kill:") {
		name := strings.TrimPrefix(response, "kill:")
		return &RouterIntent{Action: "kill", Name: strings.TrimSpace(name)}, nil
	}

	// Simple intents
	switch response {
	case "status":
		return &RouterIntent{Action: "status"}, nil
	case "list":
		return &RouterIntent{Action: "list"}, nil
	case "passthrough":
		return &RouterIntent{Action: "passthrough", Message: originalText}, nil
	}

	// Default to passthrough if we can't parse the response
	return &RouterIntent{Action: "passthrough", Message: originalText}, nil
}

// routeMessage handles the routing logic for a message from Telegram.
// It's called for group messages that are NOT in a topic (general chat area)
// or for messages where the router is enabled.
// Returns true if the message was handled by the router.
func routeMessage(config *Config, chatID int64, threadID int64, text string) bool {
	intent, err := classifyIntent(config, text)
	if err != nil {
		hookLog("router: classification failed: %v, falling through", err)
		return false
	}

	hookLog("router: classified %q as %s (name=%s)", truncate(text, 50), intent.Action, intent.Name)

	switch intent.Action {
	case "new_session":
		return handleRouterNewSession(config, chatID, threadID, intent)
	case "status":
		return handleRouterStatus(config, chatID, threadID)
	case "list":
		return handleRouterStatus(config, chatID, threadID)
	case "peek":
		return handleRouterPeek(config, chatID, threadID, intent)
	case "kill":
		return handleRouterKill(config, chatID, threadID, intent)
	case "switch":
		return handleRouterSwitch(config, chatID, threadID, intent)
	case "passthrough":
		return false // Let normal message handling take over
	}

	return false
}

func handleRouterNewSession(config *Config, chatID int64, threadID int64, intent *RouterIntent) bool {
	name := intent.Name
	prompt := intent.Message

	if config.GroupID == 0 {
		sendMessage(config, chatID, threadID, "No group configured. Run: ccc setgroup")
		return true
	}

	// Check if session already exists
	if _, exists := config.Sessions[name]; exists {
		sendMessage(config, chatID, threadID, fmt.Sprintf("Session '%s' already exists. Use a different name.", name))
		return true
	}

	// Create topic
	topicID, err := createForumTopic(config, name)
	if err != nil {
		sendMessage(config, chatID, threadID, fmt.Sprintf("Failed to create topic: %v", err))
		return true
	}

	workDir := resolveProjectPath(config, name)
	config.Sessions[name] = &SessionInfo{
		TopicID: topicID,
		Path:    workDir,
	}
	saveConfig(config)

	os.MkdirAll(workDir, 0755)

	tmuxName := "claude-" + strings.ReplaceAll(name, ".", "_")
	if err := createTmuxSession(tmuxName, workDir, false); err != nil {
		sendMessage(config, config.GroupID, topicID, fmt.Sprintf("Failed to start tmux: %v", err))
		return true
	}

	// Wait for Claude and send the initial prompt
	go func() {
		if err := waitForClaude(tmuxName, 30*time.Second); err != nil {
			sendMessage(config, config.GroupID, topicID, fmt.Sprintf("Claude didn't start in time: %v", err))
			return
		}
		if prompt != "" {
			sendToTmux(tmuxName, prompt)
		}
	}()

	sendMessage(config, chatID, threadID, fmt.Sprintf("Session '%s' created! Check the new topic.", name))
	sendMessage(config, config.GroupID, topicID, fmt.Sprintf("Session '%s' started.\n\nPrompt: %s", name, prompt))
	return true
}

func handleRouterStatus(config *Config, chatID int64, threadID int64) bool {
	if len(config.Sessions) == 0 {
		sendMessage(config, chatID, threadID, "No active sessions.")
		return true
	}

	var sb strings.Builder
	sb.WriteString("Sessions:\n\n")
	for name := range config.Sessions {
		tmuxName := sessionName(name)
		status := "stopped"
		if tmuxSessionExists(tmuxName) {
			if isClaudeIdle(tmuxName) {
				status = "idle (waiting for input)"
			} else {
				status = "working..."
			}
		}
		info := config.Sessions[name]
		sb.WriteString(fmt.Sprintf("- %s [%s]\n  Path: %s\n", name, status, info.Path))
	}
	sendMessage(config, chatID, threadID, sb.String())
	return true
}

func handleRouterPeek(config *Config, chatID int64, threadID int64, intent *RouterIntent) bool {
	name := findSessionByFuzzyName(config, intent.Name)
	if name == "" {
		sendMessage(config, chatID, threadID, fmt.Sprintf("Session '%s' not found.", intent.Name))
		return true
	}

	tmuxName := sessionName(name)
	if !tmuxSessionExists(tmuxName) {
		sendMessage(config, chatID, threadID, fmt.Sprintf("Session '%s' is not running.", name))
		return true
	}

	blocks := getLastBlocksFromTmux(tmuxName)
	if len(blocks) == 0 {
		sendMessage(config, chatID, threadID, fmt.Sprintf("Session '%s': no output yet.", name))
		return true
	}

	// Show last 2 blocks max
	start := len(blocks) - 2
	if start < 0 {
		start = 0
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Peek at '%s':\n\n", name))
	for _, block := range blocks[start:] {
		sb.WriteString(block)
		sb.WriteString("\n\n")
	}
	sendMessage(config, chatID, threadID, sb.String())
	return true
}

func handleRouterKill(config *Config, chatID int64, threadID int64, intent *RouterIntent) bool {
	name := findSessionByFuzzyName(config, intent.Name)
	if name == "" {
		sendMessage(config, chatID, threadID, fmt.Sprintf("Session '%s' not found.", intent.Name))
		return true
	}

	tmuxName := sessionName(name)
	if tmuxSessionExists(tmuxName) {
		killTmuxSession(tmuxName)
	}

	ClearSessionMonitor(name)
	sendMessage(config, chatID, threadID, fmt.Sprintf("Session '%s' killed.", name))
	return true
}

func handleRouterSwitch(config *Config, chatID int64, threadID int64, intent *RouterIntent) bool {
	name := findSessionByFuzzyName(config, intent.Name)
	if name == "" {
		sendMessage(config, chatID, threadID, fmt.Sprintf("Session '%s' not found.", intent.Name))
		return true
	}

	info := config.Sessions[name]
	if info == nil {
		sendMessage(config, chatID, threadID, fmt.Sprintf("Session '%s' has no topic.", name))
		return true
	}

	sendMessage(config, chatID, threadID, fmt.Sprintf("Session '%s' is in topic %d. Send messages there to interact.", name, info.TopicID))
	return true
}

// findSessionByFuzzyName tries to find a session by exact name first,
// then by prefix match, then by substring match.
func findSessionByFuzzyName(config *Config, query string) string {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return ""
	}

	// Exact match
	for name := range config.Sessions {
		if strings.ToLower(name) == query {
			return name
		}
	}

	// Prefix match
	for name := range config.Sessions {
		if strings.HasPrefix(strings.ToLower(name), query) {
			return name
		}
	}

	// Substring match
	for name := range config.Sessions {
		if strings.Contains(strings.ToLower(name), query) {
			return name
		}
	}

	return ""
}
