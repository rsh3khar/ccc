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
	tmuxSocket string
	tmuxPath   string
	cccPath    string
	claudePath string
)

func initPaths() {
	// Find tmux socket path using current UID
	// macOS uses /private/tmp, Linux uses /tmp
	uid := os.Getuid()
	macOSSocket := fmt.Sprintf("/private/tmp/tmux-%d/default", uid)
	linuxSocket := fmt.Sprintf("/tmp/tmux-%d/default", uid)

	// Check which socket exists, prefer Linux path first (more common in headless)
	if _, err := os.Stat(linuxSocket); err == nil {
		tmuxSocket = linuxSocket
	} else if _, err := os.Stat(macOSSocket); err == nil {
		tmuxSocket = macOSSocket
	} else {
		// Default based on OS
		if _, err := os.Stat("/private"); err == nil {
			tmuxSocket = macOSSocket
		} else {
			tmuxSocket = linuxSocket
		}
	}

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

	// Find ccc binary (self)
	if exe, err := os.Executable(); err == nil {
		cccPath = exe
	}

	// Find claude binary - first try PATH, then fallback paths
	if path, err := exec.LookPath("claude"); err == nil {
		claudePath = path
	} else {
		home, _ := os.UserHomeDir()
		claudePaths := []string{
			home + "/.claude/local/claude",
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
	// Use = prefix to force exact session name matching (avoids issues with dots in names)
	cmd := exec.Command(tmuxPath, "-S", tmuxSocket, "has-session", "-t", "="+name)
	return cmd.Run() == nil
}

func createTmuxSession(name string, workDir string, continueSession bool) error {
	// Build the command to run inside tmux
	cccCmd := cccPath + " run"
	if continueSession {
		cccCmd += " -c"
	}

	// Create tmux session with a login shell (don't run command directly - it kills session on exit)
	args := []string{"-S", tmuxSocket, "new-session", "-d", "-s", name, "-c", workDir}
	cmd := exec.Command(tmuxPath, args...)
	if err := cmd.Run(); err != nil {
		return err
	}

	// Enable mouse mode for this session (allows scrolling)
	// Use = prefix to force exact session name matching (avoids issues with dots in names)
	exec.Command(tmuxPath, "-S", tmuxSocket, "set-option", "-t", "="+name, "mouse", "on").Run()

	// Send the command to the session via send-keys (preserves TTY properly)
	time.Sleep(200 * time.Millisecond)
	exec.Command(tmuxPath, "-S", tmuxSocket, "send-keys", "-t", "="+name, cccCmd, "C-m").Run()

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

	return cmd.Run()
}

func sendToTmux(session string, text string) error {
	return sendToTmuxWithDelay(session, text, 50*time.Millisecond)
}

func sendToTmuxWithDelay(session string, text string, delay time.Duration) error {
	// Use = prefix to force exact session name matching (avoids issues with dots in names)
	target := "=" + session

	// Send text literally
	cmd := exec.Command(tmuxPath, "-S", tmuxSocket, "send-keys", "-t", target, "-l", text)
	if err := cmd.Run(); err != nil {
		return err
	}

	// Wait for content to load (e.g., images)
	time.Sleep(delay)

	// Send Enter twice (Claude Code needs double Enter)
	cmd = exec.Command(tmuxPath, "-S", tmuxSocket, "send-keys", "-t", target, "C-m")
	if err := cmd.Run(); err != nil {
		return err
	}
	time.Sleep(50 * time.Millisecond)
	cmd = exec.Command(tmuxPath, "-S", tmuxSocket, "send-keys", "-t", target, "C-m")
	return cmd.Run()
}

func killTmuxSession(name string) error {
	// Use = prefix to force exact session name matching (avoids issues with dots in names)
	cmd := exec.Command(tmuxPath, "-S", tmuxSocket, "kill-session", "-t", "="+name)
	return cmd.Run()
}

func listTmuxSessions() ([]string, error) {
	cmd := exec.Command(tmuxPath, "-S", tmuxSocket, "list-sessions", "-F", "#{session_name}")
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
