package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIsBulletLine(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"⏺ This is a block", true},
		{"● Another block", true},
		{"✻ Special block", true},
		{"Normal text", false},
		{"", false},
		{"  ⏺ With leading space", false}, // trimmed before calling
		{"⏺", true},                        // just bullet matches prefix
		{"⏺  Double space", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := isBulletLine(tt.input)
			if result != tt.expected {
				t.Errorf("isBulletLine(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestIsStatusLine(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"✱ Hashing...", true},
		{"✢ Thinking...", true},
		{"✽ Other status", true},
		{"✽ Spinning… (32s · ↓ 1.6k tokens · thinking)", true},
		{"+ Progress", true},
		{"* Alternative", true},
		{"Normal text", false},
		{"⏺ Block not status", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := isStatusLine(tt.input)
			if result != tt.expected {
				t.Errorf("isStatusLine(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestBlocksEqual(t *testing.T) {
	tests := []struct {
		name     string
		a        []string
		b        []string
		expected bool
	}{
		{"both empty", []string{}, []string{}, true},
		{"both nil", nil, nil, true},
		{"one nil one empty", nil, []string{}, true},
		{"same content", []string{"a", "b"}, []string{"a", "b"}, true},
		{"different content", []string{"a", "b"}, []string{"a", "c"}, false},
		{"different length", []string{"a", "b"}, []string{"a"}, false},
		{"same length different", []string{"a"}, []string{"b"}, false},
		{"whitespace normalized", []string{"  a  "}, []string{"a"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := blocksEqual(tt.a, tt.b)
			if result != tt.expected {
				t.Errorf("blocksEqual(%v, %v) = %v, want %v", tt.a, tt.b, result, tt.expected)
			}
		})
	}
}

func TestBlockHash(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"short text", "hello", "hello"},
		{"with whitespace", "  hello  ", "hello"},
		{"exactly 100 chars", strings.Repeat("a", 100), strings.Repeat("a", 100)},
		{"over 100 chars truncates", strings.Repeat("a", 150), strings.Repeat("a", 100)},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := blockHash(tt.input)
			if result != tt.expected {
				t.Errorf("blockHash() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestExtractBlocks(t *testing.T) {
	tests := []struct {
		name     string
		lines    []string
		start    int
		end      int
		expected []string
	}{
		{
			name: "single block",
			lines: []string{
				"❯ user input",
				"⏺ Response block",
				"  continued line",
			},
			start:    1,
			end:      3,
			expected: []string{"Response block\ncontinued line"},
		},
		{
			name: "multiple blocks",
			lines: []string{
				"❯ input",
				"⏺ First block",
				"⏺ Second block",
			},
			start:    1,
			end:      3,
			expected: []string{"First block", "Second block"},
		},
		{
			name: "skips status line and continues",
			lines: []string{
				"❯ input",
				"⏺ Block before",
				"✱ Thinking...",
				"⏺ Block after status",
			},
			start:    1,
			end:      4,
			expected: []string{"Block before", "Block after status"},
		},
		{
			name: "stops at final input box",
			lines: []string{
				"❯ input",
				"⏺ Block",
				"────────────────",
				"❯",
				"────────────────",
			},
			start:    1,
			end:      5,
			expected: []string{"Block"},
		},
		{
			name: "skips separator not followed by prompt",
			lines: []string{
				"❯ input",
				"⏺ Block start",
				"────────────────",
				"  continuation",
				"⏺ Next block",
			},
			start:    1,
			end:      5,
			expected: []string{"Block start", "Next block"},
		},
		{
			name:     "empty range",
			lines:    []string{"❯ input"},
			start:    1,
			end:      1,
			expected: nil,
		},
		{
			name: "skips non-bullet lines before first block",
			lines: []string{
				"❯ input",
				"Some random text",
				"⏺ Actual block",
			},
			start:    1,
			end:      3,
			expected: []string{"Actual block"},
		},
		{
			name: "multiline block with empty lines",
			lines: []string{
				"❯ input",
				"⏺ Block start",
				"  middle line",
				"",
				"  after empty",
			},
			start:    1,
			end:      5,
			expected: []string{"Block start\nmiddle line\n\nafter empty"},
		},
		{
			name: "skips bottom status line",
			lines: []string{
				"❯ input",
				"⏺ Block",
				"⏵⏵ bypass permissions on",
			},
			start:    1,
			end:      3,
			expected: []string{"Block"},
		},
		{
			name: "handles real Claude output format",
			lines: []string{
				"❯ fix the bug",
				"⏺ Looking at the code...",
				"",
				"  I see the issue.",
				"⏺ Read 2 files (ctrl+o to expand)",
				"⏺ The problem is in line 42.",
				"✽ Spinning… (5s)",
				"────────────────",
				"❯",
				"────────────────",
			},
			start:    1,
			end:      10,
			expected: []string{"Looking at the code...\n\nI see the issue.", "Read 2 files (ctrl+o to expand)", "The problem is in line 42."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractBlocks(tt.lines, tt.start, tt.end)
			if !blocksEqual(result, tt.expected) {
				t.Errorf("extractBlocks() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestBlockCache(t *testing.T) {
	// Use temp directory
	tmpDir, err := os.MkdirTemp("", "ccc-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Override temp dir for cache
	originalTmp := os.Getenv("TMPDIR")
	os.Setenv("TMPDIR", tmpDir)
	defer os.Setenv("TMPDIR", originalTmp)

	sessionName := "test-session"
	cacheFile := filepath.Join(tmpDir, "ccc-blocks-"+sessionName+".json")

	// Test load non-existent returns empty
	cache := loadBlockCache(sessionName)
	if len(cache.Blocks) != 0 {
		t.Errorf("loadBlockCache for non-existent = %d blocks, want 0", len(cache.Blocks))
	}

	// Test save and load with new hash-based format
	cache.Blocks = []CachedBlock{
		{Text: "block1", MsgID: 100, Hash: "block1"},
		{Text: "block2", MsgID: 200, Hash: "block2"},
	}
	cache.Hashes = map[string]int64{
		"block1": 100,
		"block2": 200,
	}
	saveBlockCache(sessionName, cache)

	// Verify file exists
	if _, err := os.Stat(cacheFile); os.IsNotExist(err) {
		t.Error("Cache file was not created")
	}

	// Load and verify
	loaded := loadBlockCache(sessionName)
	if len(loaded.Blocks) != 2 {
		t.Errorf("loaded cache has %d blocks, want 2", len(loaded.Blocks))
	}
	if loaded.Blocks[0].Text != "block1" || loaded.Blocks[0].MsgID != 100 {
		t.Errorf("Block 0 mismatch: %+v", loaded.Blocks[0])
	}
	if loaded.Hashes == nil {
		t.Error("Hashes should be loaded")
	}
	if loaded.Hashes["block1"] != 100 {
		t.Errorf("Hash lookup failed: got %d, want 100", loaded.Hashes["block1"])
	}

	// Test clear
	clearBlockCache(sessionName)
	if _, err := os.Stat(cacheFile); !os.IsNotExist(err) {
		t.Error("Cache file should be deleted after clear")
	}
}

func TestBlockCacheInvalidJSON(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "ccc-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	originalTmp := os.Getenv("TMPDIR")
	os.Setenv("TMPDIR", tmpDir)
	defer os.Setenv("TMPDIR", originalTmp)

	// Write invalid JSON
	cacheFile := filepath.Join(tmpDir, "ccc-blocks-invalid.json")
	os.WriteFile(cacheFile, []byte("not valid json{{{"), 0600)

	// Should return empty cache, not error
	cache := loadBlockCache("invalid")
	if len(cache.Blocks) != 0 {
		t.Error("Invalid JSON should return empty cache")
	}
}

func TestRemoveBulletPrefix(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"⏺ Text", "Text"},
		{"⏺  Double space", "Double space"},
		{"● Text", "Text"},
		{"✻ Text", "Text"},
		{"No bullet", "No bullet"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := removeBulletPrefix(tt.input)
			if result != tt.expected {
				t.Errorf("removeBulletPrefix(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSessionMonitorReset(t *testing.T) {
	// Clear any existing monitors
	monitorsMu.Lock()
	monitors = make(map[string]*SessionMonitor)
	monitorsMu.Unlock()

	sessionName := "test-session"

	// Reset non-existent session should create it
	ResetSessionMonitor(sessionName)

	monitorsMu.Lock()
	mon, exists := monitors[sessionName]
	monitorsMu.Unlock()

	if !exists {
		t.Fatal("ResetSessionMonitor should create monitor if not exists")
	}
	if mon.Completed {
		t.Error("New monitor should not be completed")
	}
	if mon.LastBlocks != nil {
		t.Error("New monitor should have nil LastBlocks")
	}

	// Set some state
	monitorsMu.Lock()
	mon.Completed = true
	mon.StableCount = 10
	mon.LastBlocks = []string{"old", "blocks"}
	monitorsMu.Unlock()

	// Reset should clear state
	ResetSessionMonitor(sessionName)

	monitorsMu.Lock()
	if mon.Completed {
		t.Error("Reset should set Completed = false")
	}
	if mon.StableCount != 0 {
		t.Error("Reset should set StableCount = 0")
	}
	if mon.LastBlocks != nil {
		t.Error("Reset should set LastBlocks = nil")
	}
	monitorsMu.Unlock()
}

func TestClearSessionMonitor(t *testing.T) {
	// Clear and set up test state
	monitorsMu.Lock()
	monitors = make(map[string]*SessionMonitor)
	monitors["test-session"] = &SessionMonitor{
		Completed:   true,
		StableCount: 5,
	}
	monitorsMu.Unlock()

	ClearSessionMonitor("test-session")

	monitorsMu.Lock()
	_, exists := monitors["test-session"]
	monitorsMu.Unlock()

	if exists {
		t.Error("ClearSessionMonitor should delete the monitor")
	}
}

func TestSessionMonitorTimestamps(t *testing.T) {
	monitorsMu.Lock()
	monitors = make(map[string]*SessionMonitor)
	monitorsMu.Unlock()

	before := time.Now()
	ResetSessionMonitor("timestamp-test")
	after := time.Now()

	monitorsMu.Lock()
	mon := monitors["timestamp-test"]
	monitorsMu.Unlock()

	if mon.LastUserMessage.Before(before) || mon.LastUserMessage.After(after) {
		t.Error("LastUserMessage should be set to current time")
	}
	if mon.LastActivity.Before(before) || mon.LastActivity.After(after) {
		t.Error("LastActivity should be set to current time")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"longer than ten", 10, "longer tha..."},
		{"", 10, ""},
		{"test", 0, "..."},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := truncate(tt.input, tt.maxLen)
			if result != tt.expected {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, result, tt.expected)
			}
		})
	}
}

func TestSessionMonitorStruct(t *testing.T) {
	mon := &SessionMonitor{
		LastBlocks:      []string{"a", "b"},
		StableCount:     5,
		Completed:       true,
		LastPromptIdx:   3,
		LastUserMessage: time.Now(),
		LastActivity:    time.Now(),
		SlowPollCounter: 10,
	}

	// Verify all fields are accessible
	if len(mon.LastBlocks) != 2 {
		t.Error("LastBlocks field not working")
	}
	if mon.StableCount != 5 {
		t.Error("StableCount field not working")
	}
	if !mon.Completed {
		t.Error("Completed field not working")
	}
	if mon.LastPromptIdx != 3 {
		t.Error("LastPromptIdx field not working")
	}
	if mon.SlowPollCounter != 10 {
		t.Error("SlowPollCounter field not working")
	}
}

func TestCachedBlockStruct(t *testing.T) {
	block := CachedBlock{
		Text:  "test text",
		MsgID: 12345,
		Hash:  "test text",
	}

	if block.Text != "test text" {
		t.Error("Text field not working")
	}
	if block.MsgID != 12345 {
		t.Error("MsgID field not working")
	}
	if block.Hash != "test text" {
		t.Error("Hash field not working")
	}
}

func TestBlockCacheStruct(t *testing.T) {
	cache := &BlockCache{
		Blocks: []CachedBlock{{Text: "a", MsgID: 1, Hash: "a"}},
		Hashes: map[string]int64{"a": 1},
	}

	if len(cache.Blocks) != 1 {
		t.Error("Blocks field not working")
	}
	if cache.Hashes["a"] != 1 {
		t.Error("Hashes field not working")
	}
}

func TestExtractBlocksEdgeCases(t *testing.T) {
	// Test with lines containing only whitespace
	lines := []string{
		"❯ input",
		"⏺ Block",
		"   ",
		"  more content",
	}
	result := extractBlocks(lines, 1, 4)
	if len(result) != 1 {
		t.Errorf("Expected 1 block, got %d", len(result))
	}

	// Test with multiple consecutive bullets
	lines2 := []string{
		"❯ input",
		"⏺ A",
		"⏺ B",
		"⏺ C",
	}
	result2 := extractBlocks(lines2, 1, 4)
	if len(result2) != 3 {
		t.Errorf("Expected 3 blocks, got %d", len(result2))
	}

	// Test with status line at start
	lines3 := []string{
		"❯ input",
		"✱ Loading...",
		"⏺ Block",
	}
	result3 := extractBlocks(lines3, 1, 3)
	if len(result3) != 1 {
		t.Errorf("Expected 1 block after skipping status, got %d", len(result3))
	}
}

func TestExtractBlocksRealWorldScenario(t *testing.T) {
	// Simulate real Claude Code output during active work
	lines := []string{
		"❯ help me refactor this code",
		"",
		"⏺ I'll help you refactor this code. Let me first understand what we're working",
		"  with.",
		"",
		"⏺ Read 3 files (ctrl+o to expand)",
		"",
		"⏺ I can see the code structure. Here's my plan:",
		"",
		"  1. Extract the validation logic",
		"  2. Create a new helper function",
		"  3. Update the tests",
		"",
		"⏺ Let me start with the first change:",
		"",
		"⏺ Edit(main.go)",
		"  ⎿  Updated main.go",
		"",
		"✽ Spinning… (10s · thinking)",
		"",
		"───────────────────────────────────────────",
		"❯",
		"───────────────────────────────────────────",
		"  ⏵⏵ bypass permissions",
	}

	result := extractBlocks(lines, 1, len(lines))

	// Should extract all content blocks, skip status, stop at input box
	expected := []string{
		"I'll help you refactor this code. Let me first understand what we're working\nwith.",
		"Read 3 files (ctrl+o to expand)",
		"I can see the code structure. Here's my plan:\n\n1. Extract the validation logic\n2. Create a new helper function\n3. Update the tests",
		"Let me start with the first change:",
		"Edit(main.go)\n⎿  Updated main.go",
	}

	if len(result) != len(expected) {
		t.Errorf("Expected %d blocks, got %d: %v", len(expected), len(result), result)
		return
	}

	for i, exp := range expected {
		if strings.TrimSpace(result[i]) != strings.TrimSpace(exp) {
			t.Errorf("Block %d mismatch:\ngot:  %q\nwant: %q", i, result[i], exp)
		}
	}
}

func TestMonitorMutexSafety(t *testing.T) {
	// Test concurrent access to monitors
	monitorsMu.Lock()
	monitors = make(map[string]*SessionMonitor)
	monitorsMu.Unlock()

	done := make(chan bool)

	// Concurrent resets
	for i := 0; i < 10; i++ {
		go func(n int) {
			ResetSessionMonitor("concurrent-test")
			done <- true
		}(i)
	}

	// Concurrent clears
	for i := 0; i < 10; i++ {
		go func(n int) {
			ClearSessionMonitor("concurrent-test")
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 20; i++ {
		<-done
	}

	// Should not panic or deadlock
}
