package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SessionMonitor tracks the state of each session for polling
type SessionMonitor struct {
	LastBlocks    []string  // blocks from last poll
	StableCount   int       // how many consecutive polls blocks haven't changed
	Completed     bool      // whether we've already sent ✅
	LastPromptIdx int       // track which prompt we're on
}

var (
	monitors   = make(map[string]*SessionMonitor)
	monitorsMu sync.Mutex
)

// BlockCache stores the mapping of terminal blocks to Telegram messages
type BlockCache struct {
	Blocks []CachedBlock `json:"blocks"`
}

type CachedBlock struct {
	Text  string `json:"text"`
	MsgID int64  `json:"msg_id"`
}

func loadBlockCache(sessionName string) *BlockCache {
	cacheFile := filepath.Join(os.TempDir(), "ccc-blocks-"+sessionName+".json")
	data, err := os.ReadFile(cacheFile)
	if err != nil {
		return &BlockCache{}
	}
	var cache BlockCache
	if json.Unmarshal(data, &cache) != nil {
		return &BlockCache{}
	}
	return &cache
}

func saveBlockCache(sessionName string, cache *BlockCache) {
	cacheFile := filepath.Join(os.TempDir(), "ccc-blocks-"+sessionName+".json")
	data, _ := json.Marshal(cache)
	os.WriteFile(cacheFile, data, 0600)
}

func clearBlockCache(sessionName string) {
	cacheFile := filepath.Join(os.TempDir(), "ccc-blocks-"+sessionName+".json")
	os.Remove(cacheFile)
}

// getLastBlocksFromTmux captures the tmux pane and extracts assistant blocks
// after the last user prompt (❯). Each block starts with ⏺ and ends at the
// next ⏺ or the input box (────).
func getLastBlocksFromTmux(tmuxSession string) []string {
	cmd := exec.Command(tmuxPath, "capture-pane", "-t", tmuxSession, "-p", "-S", "-500")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	lines := strings.Split(string(output), "\n")

	// Find the last user prompt (❯) before the input box
	lastPromptIdx := -1
	inputBoxIdx := len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "────") {
			inputBoxIdx = i
			continue
		}
		if strings.HasPrefix(trimmed, "❯") && i < inputBoxIdx {
			content := strings.TrimSpace(strings.TrimPrefix(trimmed, "❯"))
			if content == "" && lastPromptIdx == -1 {
				// Empty prompt at the end, keep looking
				continue
			}
			lastPromptIdx = i
			break
		}
	}

	if lastPromptIdx == -1 {
		return nil
	}

	// Extract blocks between lastPromptIdx and inputBoxIdx
	var blocks []string
	var currentBlock strings.Builder
	inBlock := false

	for i := lastPromptIdx + 1; i < inputBoxIdx; i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// Detect block start: ⏺ (U+23FA) or ● or ✻
		if strings.HasPrefix(trimmed, "⏺") || strings.HasPrefix(line, "● ") || strings.HasPrefix(line, "✻ ") {
			if inBlock && currentBlock.Len() > 0 {
				blocks = append(blocks, strings.TrimSpace(currentBlock.String()))
			}
			currentBlock.Reset()
			// Remove the bullet prefix
			blockText := trimmed
			for _, prefix := range []string{"⏺ ", "⏺  ", "● ", "✻ "} {
				if strings.HasPrefix(blockText, prefix) {
					blockText = strings.TrimPrefix(blockText, prefix)
					break
				}
			}
			currentBlock.WriteString(blockText)
			inBlock = true
		} else if inBlock {
			if trimmed == "" {
				currentBlock.WriteString("\n")
			} else {
				currentBlock.WriteString("\n")
				currentBlock.WriteString(trimmed)
			}
		}
	}

	// Don't forget the last block
	if inBlock && currentBlock.Len() > 0 {
		blocks = append(blocks, strings.TrimSpace(currentBlock.String()))
	}

	return blocks
}

// isClaudeIdle checks if Claude is waiting for input (❯ prompt visible)
func isClaudeIdle(tmuxSession string) bool {
	cmd := exec.Command(tmuxPath, "capture-pane", "-t", tmuxSession, "-p", "-S", "-10")
	output, err := cmd.Output()
	if err != nil {
		return false
	}

	lines := strings.Split(string(output), "\n")
	// Look for input box (────) followed by empty prompt (❯)
	foundInputBox := false
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "❯") && foundInputBox {
			return true
		}
		if strings.HasPrefix(trimmed, "────") {
			foundInputBox = true
			continue
		}
		if foundInputBox {
			// Something between input box and bottom that's not ❯
			return false
		}
	}
	return false
}

// syncBlocksToTelegram parses the tmux terminal and syncs blocks to Telegram.
func syncBlocksToTelegram(config *Config, sessionName string, topicID int64, isFinal bool) int {
	tmuxName := "claude-" + sessionName
	blocks := getLastBlocksFromTmux(tmuxName)
	if len(blocks) == 0 {
		return 0
	}

	cache := loadBlockCache(sessionName)

	for i, block := range blocks {
		displayText := block
		if isFinal && i == len(blocks)-1 {
			displayText = "✅ " + sessionName + "\n\n" + block
		}

		if i < len(cache.Blocks) {
			// Block already has a Telegram message - edit if changed
			if strings.TrimSpace(cache.Blocks[i].Text) != strings.TrimSpace(block) {
				cache.Blocks[i].Text = block
				if cache.Blocks[i].MsgID > 0 {
					editMessage(config, config.GroupID, cache.Blocks[i].MsgID, topicID, displayText)
				}
			} else if isFinal && i == len(blocks)-1 && cache.Blocks[i].MsgID > 0 {
				// Even if text didn't change, add ✅ prefix on final
				editMessage(config, config.GroupID, cache.Blocks[i].MsgID, topicID, displayText)
			}
		} else {
			// New block - send new message
			msgID, err := sendMessageGetID(config, config.GroupID, topicID, displayText)
			if err == nil && msgID > 0 {
				cache.Blocks = append(cache.Blocks, CachedBlock{Text: block, MsgID: msgID})
			}
		}
	}

	saveBlockCache(sessionName, cache)
	return len(blocks)
}

// startSessionMonitor runs a background goroutine that polls all active tmux
// sessions every few seconds, parses their terminal output, and syncs blocks
// to Telegram.
func startSessionMonitor(config *Config) {
	ticker := time.NewTicker(7 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		// Reload config to pick up new sessions
		freshConfig, err := loadConfig()
		if err != nil {
			continue
		}

		for sessName, info := range freshConfig.Sessions {
			if info == nil || info.TopicID == 0 || freshConfig.GroupID == 0 {
				continue
			}

			tmuxName := "claude-" + sessName
			if !tmuxSessionExists(tmuxName) {
				continue
			}

			monitorsMu.Lock()
			mon, exists := monitors[sessName]
			if !exists {
				mon = &SessionMonitor{}
				monitors[sessName] = mon
			}
			monitorsMu.Unlock()

			blocks := getLastBlocksFromTmux(tmuxName)

			// No blocks = nothing to do
			if len(blocks) == 0 {
				if mon.Completed {
					// Still idle, nothing to do
					continue
				}
				mon.LastBlocks = nil
				mon.StableCount = 0
				continue
			}

			// Check if blocks changed
			changed := !blocksEqual(blocks, mon.LastBlocks)

			if changed {
				mon.LastBlocks = blocks
				mon.StableCount = 0
				mon.Completed = false
				// Sync intermediate state
				syncBlocksToTelegram(freshConfig, sessName, info.TopicID, false)
			} else {
				mon.StableCount++
			}

			// If blocks are stable for 2+ polls AND Claude is idle → mark complete
			if !mon.Completed && mon.StableCount >= 2 && isClaudeIdle(tmuxName) {
				n := syncBlocksToTelegram(freshConfig, sessName, info.TopicID, true)
				if n == 0 {
					sendMessage(freshConfig, freshConfig.GroupID, info.TopicID, fmt.Sprintf("✅ %s", sessName))
				}
				mon.Completed = true
			}
		}
	}
}

// ResetSessionMonitor clears the monitor state for a session (called when user sends a new message)
func ResetSessionMonitor(sessionName string) {
	monitorsMu.Lock()
	defer monitorsMu.Unlock()
	delete(monitors, sessionName)
	clearBlockCache(sessionName)
}

func blocksEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.TrimSpace(a[i]) != strings.TrimSpace(b[i]) {
			return false
		}
	}
	return true
}
