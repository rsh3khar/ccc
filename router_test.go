package main

import (
	"testing"
)

func TestParseIntent(t *testing.T) {
	tests := []struct {
		name         string
		response     string
		originalText string
		wantAction   string
		wantName     string
		wantMessage  string
	}{
		{
			name:         "new session with name and prompt",
			response:     "new_session:quantum-research:research quantum computing",
			originalText: "start a new session to research quantum computing",
			wantAction:   "new_session",
			wantName:     "quantum-research",
			wantMessage:  "research quantum computing",
		},
		{
			name:         "new session with only name",
			response:     "new_session:my-project",
			originalText: "create a session called my-project",
			wantAction:   "new_session",
			wantName:     "my-project",
			wantMessage:  "create a session called my-project",
		},
		{
			name:         "status",
			response:     "status",
			originalText: "what's the status",
			wantAction:   "status",
		},
		{
			name:         "list",
			response:     "list",
			originalText: "list all sessions",
			wantAction:   "list",
		},
		{
			name:         "peek at session",
			response:     "peek:research",
			originalText: "check on the research session",
			wantAction:   "peek",
			wantName:     "research",
		},
		{
			name:         "kill session",
			response:     "kill:quantum-research",
			originalText: "stop the quantum session",
			wantAction:   "kill",
			wantName:     "quantum-research",
		},
		{
			name:         "switch session",
			response:     "switch:my-project",
			originalText: "switch to my-project",
			wantAction:   "switch",
			wantName:     "my-project",
		},
		{
			name:         "passthrough",
			response:     "passthrough",
			originalText: "fix the bug in auth.go",
			wantAction:   "passthrough",
			wantMessage:  "fix the bug in auth.go",
		},
		{
			name:         "send message",
			response:     "send:please add error handling",
			originalText: "send please add error handling",
			wantAction:   "send",
			wantMessage:  "please add error handling",
		},
		{
			name:         "unknown response defaults to passthrough",
			response:     "something_weird",
			originalText: "hello world",
			wantAction:   "passthrough",
			wantMessage:  "hello world",
		},
		{
			name:         "whitespace trimmed",
			response:     "  status  ",
			originalText: "status please",
			wantAction:   "status",
		},
		{
			name:         "empty name defaults to session",
			response:     "new_session::",
			originalText: "start a new session",
			wantAction:   "new_session",
			wantName:     "session",
			wantMessage:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent, err := parseIntent(tt.response, tt.originalText)
			if err != nil {
				t.Fatalf("parseIntent returned error: %v", err)
			}
			if intent.Action != tt.wantAction {
				t.Errorf("Action = %q, want %q", intent.Action, tt.wantAction)
			}
			if tt.wantName != "" && intent.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", intent.Name, tt.wantName)
			}
			if tt.wantMessage != "" && intent.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", intent.Message, tt.wantMessage)
			}
		})
	}
}

func TestFindSessionByFuzzyName(t *testing.T) {
	config := &Config{
		Sessions: map[string]*SessionInfo{
			"quantum-research": {TopicID: 100, Path: "/home/user/quantum-research"},
			"my-project":      {TopicID: 200, Path: "/home/user/my-project"},
			"bug-fix-auth":    {TopicID: 300, Path: "/home/user/bug-fix-auth"},
		},
	}

	tests := []struct {
		name     string
		query    string
		expected string
	}{
		{"exact match", "quantum-research", "quantum-research"},
		{"exact match case insensitive", "Quantum-Research", "quantum-research"},
		{"prefix match", "quantum", "quantum-research"},
		{"substring match", "auth", "bug-fix-auth"},
		{"no match", "nonexistent", ""},
		{"empty query", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := findSessionByFuzzyName(config, tt.query)
			if result != tt.expected {
				t.Errorf("findSessionByFuzzyName(%q) = %q, want %q", tt.query, result, tt.expected)
			}
		})
	}
}

func TestClassifyIntentNoKey(t *testing.T) {
	config := &Config{OpenRouterKey: ""}
	intent, err := classifyIntent(config, "hello world")
	if err != nil {
		t.Fatalf("classifyIntent returned error: %v", err)
	}
	if intent.Action != "passthrough" {
		t.Errorf("Action = %q, want passthrough when no key configured", intent.Action)
	}
	if intent.Message != "hello world" {
		t.Errorf("Message = %q, want original text", intent.Message)
	}
}
