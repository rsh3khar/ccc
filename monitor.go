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
	LastBlocks      []string  // blocks from last poll
	StableCount     int       // how many consecutive polls blocks haven't changed
	Completed       bool      // whether we've already sent ✅
	LastPromptIdx   int       // track which prompt we're on
	LastUserMessage time.Time // when user last sent a message (for slow polling)
	LastActivity    time.Time // last time blocks changed or new blocks appeared
	SlowPollCounter int       // counter for slow polling (poll every 10th tick = 30s)
}

var (
	monitors   = make(map[string]*SessionMonitor)
	monitorsMu sync.Mutex
)

// BlockCache stores the mapping of terminal blocks to Telegram messages
// Uses content hash for deduplication instead of position
type BlockCache struct {
	Blocks []CachedBlock `json:"blocks"`
	Hashes map[string]int64 `json:"hashes"` // hash -> msgID for dedup
}

type CachedBlock struct {
	Text  string `json:"text"`
	MsgID int64  `json:"msg_id"`
	Hash  string `json:"hash"`
}

// blockHash returns a hash of the first 100 chars of a block for deduplication
func blockHash(text string) string {
	normalized := strings.TrimSpace(text)
	if len(normalized) > 100 {
		normalized = normalized[:100]
	}
	return normalized
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
// after the last user prompt (❯) that has response blocks. Each block starts
// with ● and ends at the next ● or the input box (────).
func getLastBlocksFromTmux(tmuxSession string) []string {
	cmd := exec.Command(tmuxPath, "capture-pane", "-t", tmuxSession, "-p", "-S", "-500")
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	lines := strings.Split(string(output), "\n")

	// Collect all ❯ prompt positions and ──── input box positions
	var prompts []int   // indices of ❯ lines with content
	var inputBoxes []int // indices of ──── lines

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "───") {
			inputBoxes = append(inputBoxes, i)
		} else if strings.HasPrefix(trimmed, "❯") {
			content := strings.TrimSpace(strings.TrimPrefix(trimmed, "❯"))
			// Also trim non-breaking space (U+00A0) which Claude Code uses
			content = strings.TrimSpace(strings.ReplaceAll(content, "\u00a0", ""))
			// Skip ❯ prompts inside the input box (between two ──── lines)
			insideInputBox := false
			for _, ib := range inputBoxes {
				if ib == i-1 {
					insideInputBox = true
					break
				}
			}
			if content != "" && !insideInputBox {
				prompts = append(prompts, i)
			}
		}
	}

	if len(prompts) == 0 {
		return nil
	}

	// Try each prompt from most recent to oldest, return first one with blocks
	hookLog("parser: %d prompts, %d inputBoxes, %d total lines", len(prompts), len(inputBoxes), len(lines))
	for p := len(prompts) - 1; p >= 0; p-- {
		promptIdx := prompts[p]

		// Find the next user prompt after this one (or end of capture)
		// We don't stop at ─── lines because blocks can appear between
		// input box separators (e.g. after a diff block with ─── borders)
		endIdx := len(lines)
		for pp := p + 1; pp < len(prompts); pp++ {
			endIdx = prompts[pp]
			break
		}

		hookLog("parser: trying prompt %d at line %d (end %d): %s", p, promptIdx, endIdx, truncate(strings.TrimSpace(lines[promptIdx]), 40))
		blocks := extractBlocks(lines, promptIdx+1, endIdx)
		hookLog("parser: found %d blocks", len(blocks))
		if len(blocks) > 0 {
			return blocks
		}
	}

	return nil
}

// extractBlocks extracts ● bullet blocks from lines[start:end]
// Skips status lines (spinners) but continues parsing - they appear during work, not after
func extractBlocks(lines []string, start, end int) []string {
	var blocks []string
	var currentBlock strings.Builder
	inBlock := false

	for i := start; i < end; i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// Stop at input box (the final ─── before the empty prompt)
		if strings.HasPrefix(trimmed, "───") {
			// Check if this is the final input box (followed by empty ❯)
			// by looking at the next few lines
			isFinalInputBox := false
			for j := i + 1; j < end && j < i+4; j++ {
				nextTrimmed := strings.TrimSpace(lines[j])
				if nextTrimmed == "" {
					continue
				}
				if strings.HasPrefix(nextTrimmed, "❯") {
					isFinalInputBox = true
				}
				break
			}
			if isFinalInputBox {
				break
			}
			// Not the final input box - close current block but continue looking for more
			if inBlock && currentBlock.Len() > 0 {
				blocks = append(blocks, strings.TrimSpace(currentBlock.String()))
				currentBlock.Reset()
				inBlock = false
			}
			continue
		}

		// Skip status indicators (spinners) - they appear during work
		// Don't break, just skip - there may be more content after
		if isStatusLine(trimmed) {
			continue
		}

		// Skip bottom status line and ❯ prompts
		if strings.HasPrefix(trimmed, "⏵⏵") || strings.HasPrefix(trimmed, "❯") {
			continue
		}

		if isBulletLine(trimmed) {
			if inBlock && currentBlock.Len() > 0 {
				blocks = append(blocks, strings.TrimSpace(currentBlock.String()))
			}
			currentBlock.Reset()
			blockText := removeBulletPrefix(trimmed)
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

	if inBlock && currentBlock.Len() > 0 {
		blocks = append(blocks, strings.TrimSpace(currentBlock.String()))
	}

	return blocks
}

func isBulletLine(trimmed string) bool {
	return strings.HasPrefix(trimmed, "⏺") ||
		strings.HasPrefix(trimmed, "● ") ||
		strings.HasPrefix(trimmed, "✻ ")
}

// isStatusLine checks for transient status indicators that should stop block capture
func isStatusLine(trimmed string) bool {
	return strings.HasPrefix(trimmed, "✱") || // Hashing, Thinking
		strings.HasPrefix(trimmed, "✢") || // Symbioting, Computing
		strings.HasPrefix(trimmed, "✽") || // Other status
		strings.HasPrefix(trimmed, "✻") || // Sautéed, etc
		strings.HasPrefix(trimmed, "+") || // Progress indicator
		strings.HasPrefix(trimmed, "*") // Alternative status
}

// isStatusBlock checks if a block content looks like a transient status message
func isStatusBlock(text string) bool {
	lower := strings.ToLower(text)
	// Skip short blocks that are just status words
	if len(text) < 50 {
		statusWords := []string{"thinking", "transfiguring", "spinning", "sautéed", "sauteed",
			"hashing", "computing", "processing", "loading", "churned", "working", "concocting"}
		for _, word := range statusWords {
			if strings.Contains(lower, word) {
				return true
			}
		}
	}
	return false
}

func removeBulletPrefix(s string) string {
	// Order matters: longer prefixes first to match correctly
	for _, prefix := range []string{"⏺  ", "⏺ ", "● ", "✻ "} {
		if strings.HasPrefix(s, prefix) {
			return strings.TrimPrefix(s, prefix)
		}
	}
	return s
}

// isClaudeIdle checks if Claude is waiting for input (empty ❯ prompt visible, no spinner)
func isClaudeIdle(tmuxSession string) bool {
	cmd := exec.Command(tmuxPath, "capture-pane", "-t", tmuxSession, "-p", "-S", "-15")
	output, err := cmd.Output()
	if err != nil {
		return false
	}

	lines := strings.Split(string(output), "\n")

	// First, check if there's an active spinner/status - if so, not idle
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-10; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if isStatusLine(trimmed) {
			return false // Still working
		}
	}

	// Look for input box (────) followed by empty prompt (❯ with no text)
	foundInputBox := false
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "❯") && foundInputBox {
			// Only idle if prompt is empty (no text after ❯)
			content := strings.TrimSpace(strings.TrimPrefix(trimmed, "❯"))
			content = strings.ReplaceAll(content, "\u00a0", "")
			return content == ""
		}
		if strings.HasPrefix(trimmed, "───") {
			foundInputBox = true
			continue
		}
		if foundInputBox {
			return false
		}
	}
	return false
}

// syncBlocksToTelegram parses the tmux terminal and syncs blocks to Telegram.
// Uses content hash for deduplication to avoid sending duplicate messages.
func syncBlocksToTelegram(config *Config, sessName string, topicID int64, isFinal bool) int {
	tmuxName := sessionName(sessName)
	blocks := getLastBlocksFromTmux(tmuxName)
	hookLog("sync: session=%s blocks=%d isFinal=%v", sessName, len(blocks), isFinal)
	if len(blocks) == 0 {
		return 0
	}

	cache := loadBlockCache(sessName)
	if cache.Hashes == nil {
		cache.Hashes = make(map[string]int64)
	}
	hookLog("sync: session=%s cacheBlocks=%d hashes=%d", sessName, len(cache.Blocks), len(cache.Hashes))

	// Track which blocks we're sending this round
	newBlocks := make([]CachedBlock, 0, len(blocks))

	for i, block := range blocks {
		// Skip blocks that look like transient status messages
		if isStatusBlock(block) {
			hookLog("sync: session=%s skipping status block: %s", sessName, truncate(block, 30))
			continue
		}

		hash := blockHash(block)
		displayText := block
		if isFinal && i == len(blocks)-1 {
			displayText = "✅ " + sessName + "\n\n" + block
		}

		// Check if we already sent this block (by hash)
		if existingMsgID, exists := cache.Hashes[hash]; exists {
			if existingMsgID == -1 {
				// Block was shown before restart - don't resend, just track it
				newBlocks = append(newBlocks, CachedBlock{Text: block, MsgID: -1, Hash: hash})
				continue
			}
			if existingMsgID > 0 {
				// Block already sent - check if content changed (for edits)
				for j := range cache.Blocks {
					if cache.Blocks[j].Hash == hash {
						if strings.TrimSpace(cache.Blocks[j].Text) != strings.TrimSpace(block) {
							// Content changed, edit the message
							cache.Blocks[j].Text = block
							editMessage(config, config.GroupID, existingMsgID, topicID, displayText)
						} else if isFinal && i == len(blocks)-1 {
							// Add ✅ prefix on final
							editMessage(config, config.GroupID, existingMsgID, topicID, displayText)
						}
						break
					}
				}
				newBlocks = append(newBlocks, CachedBlock{Text: block, MsgID: existingMsgID, Hash: hash})
				continue
			}
		}
		// New block - send it
		hookLog("sync: session=%s sending NEW block %d hash=%s", sessName, i, truncate(hash, 30))
		msgID, err := sendMessageGetID(config, config.GroupID, topicID, displayText)
		if err != nil {
			hookLog("sync: session=%s ERROR sending block %d: %v", sessName, i, err)
			newBlocks = append(newBlocks, CachedBlock{Text: block, MsgID: 0, Hash: hash})
		} else if msgID > 0 {
			hookLog("sync: session=%s block %d sent msgID=%d", sessName, i, msgID)
			cache.Hashes[hash] = msgID
			newBlocks = append(newBlocks, CachedBlock{Text: block, MsgID: msgID, Hash: hash})
		}
	}

	cache.Blocks = newBlocks
	saveBlockCache(sessName, cache)
	return len(blocks)
}

// initializeMonitors prepares all existing sessions for monitoring after a restart.
// This ensures messages sent after /update are properly forwarded.
func initializeMonitors(config *Config) {
	monitorsMu.Lock()
	defer monitorsMu.Unlock()

	for sessName, info := range config.Sessions {
		if info == nil || info.TopicID == 0 {
			continue
		}
		tmuxName := sessionName(sessName)
		if !tmuxSessionExists(tmuxName) {
			continue
		}

		// Capture current blocks so we know what's already been shown
		currentBlocks := getLastBlocksFromTmux(tmuxName)
		idle := isClaudeIdle(tmuxName)

		// Create monitor with current state
		now := time.Now()
		monitors[sessName] = &SessionMonitor{
			LastBlocks:      currentBlocks,
			LastUserMessage: now,
			LastActivity:    now,
			Completed:       idle, // Only completed if Claude is waiting for input
			StableCount:     0,
		}

		// Populate hash cache with existing blocks to prevent re-sending after restart
		// Use msgID = -1 as marker for "already shown, don't resend"
		cache := loadBlockCache(sessName)
		if cache.Hashes == nil {
			cache.Hashes = make(map[string]int64)
		}
		for _, block := range currentBlocks {
			hash := blockHash(block)
			if _, exists := cache.Hashes[hash]; !exists {
				cache.Hashes[hash] = -1 // Mark as shown but no telegram msg
				cache.Blocks = append(cache.Blocks, CachedBlock{Text: block, MsgID: -1, Hash: hash})
			}
		}
		saveBlockCache(sessName, cache)
		hookLog("monitor: initialized session=%s blocks=%d idle=%v cache=%d", sessName, len(currentBlocks), idle, len(cache.Hashes))
	}
}

// startSessionMonitor runs a background goroutine that polls all active tmux
// sessions every few seconds, parses their terminal output, and syncs blocks
// to Telegram.
func startSessionMonitor(config *Config) {
	// Initialize all existing sessions first
	initializeMonitors(config)

	ticker := time.NewTicker(3 * time.Second)
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

			tmuxName := sessionName(sessName)
			if !tmuxSessionExists(tmuxName) {
				continue
			}

			monitorsMu.Lock()
			mon, exists := monitors[sessName]
			if !exists {
				now := time.Now()
				mon = &SessionMonitor{LastActivity: now, LastUserMessage: now}
				monitors[sessName] = mon
			}
			monitorsMu.Unlock()

			// Always poll every 3s - slow polling caused missed messages
			// The completed flag prevents unnecessary syncs when idle
			_ = mon.SlowPollCounter // unused now, kept for struct compat

			blocks := getLastBlocksFromTmux(tmuxName)
			hookLog("monitor: session=%s blocks=%d firstPoll=%v", sessName, len(blocks), !exists)

			// First time seeing this session: seed with existing blocks without sending
			if !exists && len(blocks) > 0 {
				mon.LastBlocks = blocks
				mon.StableCount = 0
				// If Claude is idle, mark completed immediately
				if isClaudeIdle(tmuxName) {
					mon.Completed = true
				}
				// Populate cache so we don't re-send these blocks later
				cache := loadBlockCache(sessName)
				if len(cache.Blocks) == 0 {
					for _, b := range blocks {
						cache.Blocks = append(cache.Blocks, CachedBlock{Text: b, MsgID: 0})
					}
					saveBlockCache(sessName, cache)
				}
				hookLog("monitor: seeded session=%s with %d existing blocks (idle=%v)", sessName, len(blocks), mon.Completed)
				continue
			}

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
			hookLog("monitor: session=%s changed=%v blocks=%d lastBlocks=%d", sessName, changed, len(blocks), len(mon.LastBlocks))

			if changed {
				mon.LastBlocks = blocks
				mon.StableCount = 0
				mon.Completed = false
				mon.LastActivity = time.Now()
				// Sync intermediate state
				syncBlocksToTelegram(freshConfig, sessName, info.TopicID, false)
			} else {
				mon.StableCount++
			}

			// If blocks are stable for 3+ polls AND Claude is idle → mark complete
			// Increased from 2 to 3 polls (9s) to avoid premature completion
			idle := isClaudeIdle(tmuxName)
			hookLog("monitor: session=%s stable=%d completed=%v idle=%v", sessName, mon.StableCount, mon.Completed, idle)
			if !mon.Completed && mon.StableCount >= 3 && idle {
				n := syncBlocksToTelegram(freshConfig, sessName, info.TopicID, true)
				if n == 0 {
					sendMessage(freshConfig, freshConfig.GroupID, info.TopicID, fmt.Sprintf("✅ %s", sessName))
				}
				mon.Completed = true
			}
			// Removed: force completion after 30s stable - this caused missed messages
			// Now we only complete when truly idle
		}
	}
}

// ResetSessionMonitor marks a session as actively awaiting new output (called when user sends a message)
// This prevents the monitor from treating the session as idle/completed.
// Does NOT clear cache - hash-based dedup prevents re-sending old blocks.
func ResetSessionMonitor(sessionName string) {
	monitorsMu.Lock()
	defer monitorsMu.Unlock()

	if mon, exists := monitors[sessionName]; exists {
		mon.Completed = false
		mon.StableCount = 0
		mon.LastUserMessage = time.Now()
		mon.LastActivity = time.Now()
		// Keep LastBlocks - only new blocks will be detected as changes
	} else {
		monitors[sessionName] = &SessionMonitor{
			LastUserMessage: time.Now(),
			LastActivity:    time.Now(),
			Completed:       false,
		}
	}
	// Don't clear cache - hash dedup handles everything
}

// ClearSessionMonitor completely removes monitor state and cache (called on /continue, /new, /delete)
// Use this when the session is being restarted from scratch.
func ClearSessionMonitor(sessionName string) {
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
