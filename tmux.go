package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

var (
	tmuxPath   string
	cccPath    string
	claudePath string
)

func initPaths() {
	// Find tmux binary
	if path, err := exec.LookPath("tmux"); err == nil {
		tmuxPath = path
	} else {
		// Fallback paths for common installations
		for _, p := range []string{"/opt/homebrew/bin/tmux", "/usr/local/bin/tmux", "/usr/bin/tmux"} {
			if _, err := os.Stat(p); err == nil {
				tmuxPath = p
				break
			}
		}
	}

	// Find ccc binary - prefer ~/bin/ccc (canonical install path),
	// then PATH, then current executable as last resort
	home, _ := os.UserHomeDir()
	binCcc := home + "/bin/ccc"
	if _, err := os.Stat(binCcc); err == nil {
		cccPath = binCcc
	} else if path, err := exec.LookPath("ccc"); err == nil {
		cccPath = path
	} else if exe, err := os.Executable(); err == nil {
		cccPath = exe
	}

	// Find claude binary - first try PATH, then fallback paths
	if path, err := exec.LookPath("claude"); err == nil {
		claudePath = path
	} else {
		home, _ := os.UserHomeDir()
		claudePaths := []string{
			home + "/.local/bin/claude",
			"/usr/local/bin/claude",
		}
		for _, p := range claudePaths {
			if _, err := os.Stat(p); err == nil {
				claudePath = p
				break
			}
		}
	}
}

func tmuxSessionExists(name string) bool {
	cmd := exec.Command(tmuxPath, "has-session", "-t", name)
	return cmd.Run() == nil
}

func createTmuxSession(name string, workDir string, continueSession bool) error {
	// Build the command to run inside tmux
	cccCmd := cccPath + " run"
	if continueSession {
		cccCmd += " -c"
	}

	// Create tmux session with a login shell (don't run command directly - it kills session on exit)
	args := []string{"new-session", "-d", "-s", name, "-c", workDir}
	cmd := exec.Command(tmuxPath, args...)
	if err := cmd.Run(); err != nil {
		return err
	}

	// Enable mouse mode for this session (allows scrolling)
	exec.Command(tmuxPath, "set-option", "-t", name, "mouse", "on").Run()

	// Send the command to the session via send-keys (preserves TTY properly)
	time.Sleep(200 * time.Millisecond)
	exec.Command(tmuxPath, "send-keys", "-t", name, cccCmd, "C-m").Run()

	return nil
}

// runClaudeRaw runs claude directly (used inside tmux sessions)
func runClaudeRaw(continueSession bool) error {
	if claudePath == "" {
		return fmt.Errorf("claude binary not found")
	}

	args := []string{"--dangerously-skip-permissions"}
	if continueSession {
		args = append(args, "-c")
	}

	cmd := exec.Command(claudePath, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Ensure OAuth token is available from config if not already in environment
	if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" {
		if config, err := loadConfig(); err == nil && config.OAuthToken != "" {
			cmd.Env = append(os.Environ(), "CLAUDE_CODE_OAUTH_TOKEN="+config.OAuthToken)
		}
	}

	return cmd.Run()
}

func sendToTmux(session string, text string) error {
	// Calculate delay based on text length
	// Base: 50ms + 0.5ms per character, capped at 5 seconds
	baseDelay := 50 * time.Millisecond
	charDelay := time.Duration(len(text)) * 500 * time.Microsecond // 0.5ms per char
	delay := baseDelay + charDelay
	if delay > 5*time.Second {
		delay = 5 * time.Second
	}
	return sendToTmuxWithDelay(session, text, delay)
}

func sendToTmuxWithDelay(session string, text string, delay time.Duration) error {
	// Send text literally
	cmd := exec.Command(tmuxPath, "send-keys", "-t", session, "-l", text)
	if err := cmd.Run(); err != nil {
		return err
	}

	// Wait for content to load (e.g., images)
	time.Sleep(delay)

	// Send Enter twice (Claude Code needs double Enter)
	cmd = exec.Command(tmuxPath, "send-keys", "-t", session, "C-m")
	if err := cmd.Run(); err != nil {
		return err
	}
	time.Sleep(50 * time.Millisecond)
	cmd = exec.Command(tmuxPath, "send-keys", "-t", session, "C-m")
	return cmd.Run()
}

func killTmuxSession(name string) error {
	cmd := exec.Command(tmuxPath, "kill-session", "-t", name)
	return cmd.Run()
}

func listTmuxSessions() ([]string, error) {
	cmd := exec.Command(tmuxPath, "list-sessions", "-F", "#{session_name}")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var sessions []string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		name := scanner.Text()
		if strings.HasPrefix(name, "claude-") {
			sessions = append(sessions, strings.TrimPrefix(name, "claude-"))
		}
	}
	return sessions, nil
}
