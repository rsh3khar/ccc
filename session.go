package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func sessionName(name string) string {
	// Replace dots with underscores - tmux interprets dots as window/pane separators
	safeName := strings.ReplaceAll(name, ".", "_")
	return "claude-" + safeName
}

func createSession(config *Config, name string) error {
	// Check if session already exists
	if _, exists := config.Sessions[name]; exists {
		return fmt.Errorf("session '%s' already exists", name)
	}

	// Create Telegram topic
	topicID, err := createForumTopic(config, name)
	if err != nil {
		return fmt.Errorf("failed to create topic: %w", err)
	}

	// Create tmux session
	workDir := resolveProjectPath(config, name)
	if _, err := os.Stat(workDir); os.IsNotExist(err) {
		// Create project directory
		os.MkdirAll(workDir, 0755)
	}

	if err := createTmuxSession(sessionName(name), workDir, false); err != nil {
		return fmt.Errorf("failed to create tmux session: %w", err)
	}

	// Save mapping with full path
	config.Sessions[name] = &SessionInfo{
		TopicID: topicID,
		Path:    workDir,
	}
	if err := saveConfig(config); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	return nil
}

func killSession(config *Config, name string) error {
	if _, exists := config.Sessions[name]; !exists {
		return fmt.Errorf("session '%s' not found", name)
	}

	// Kill tmux session
	killTmuxSession(sessionName(name))

	// Remove from config
	delete(config.Sessions, name)
	saveConfig(config)

	return nil
}

func getSessionByTopic(config *Config, topicID int64) string {
	for name, info := range config.Sessions {
		if info != nil && info.TopicID == topicID {
			return name
		}
	}
	return ""
}

// startSession creates/attaches to a tmux session with Telegram topic
func startSession(continueSession bool) error {
	// Get current directory name as session name
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	name := filepath.Base(cwd)
	tmuxName := sessionName(name)

	// Load config to check/create topic
	config, err := loadConfig()
	if err != nil {
		// No config, just run claude directly
		return runClaudeRaw(continueSession)
	}

	// Create topic if it doesn't exist and we have a group configured
	if config.GroupID != 0 {
		if _, exists := config.Sessions[name]; !exists {
			topicID, err := createForumTopic(config, name)
			if err == nil {
				config.Sessions[name] = &SessionInfo{
					TopicID: topicID,
					Path:    cwd,
				}
				saveConfig(config)
				fmt.Printf("📱 Created Telegram topic: %s\n", name)
			}
		}
	}

	// Check if tmux session exists
	if tmuxSessionExists(tmuxName) {
		// Use = prefix to force exact session name matching (avoids issues with dots in names)
		target := "=" + tmuxName
		// Check if we're already inside tmux
		if os.Getenv("TMUX") != "" {
			// Inside tmux: switch to the session
			cmd := exec.Command(tmuxPath, "-S", tmuxSocket, "switch-client", "-t", target)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		}
		// Outside tmux: attach to existing session
		cmd := exec.Command(tmuxPath, "-S", tmuxSocket, "attach-session", "-t", target)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	// Create new tmux session and attach
	if err := createTmuxSession(tmuxName, cwd, continueSession); err != nil {
		return err
	}

	// Use = prefix to force exact session name matching (avoids issues with dots in names)
	target := "=" + tmuxName

	// Check if we're already inside tmux
	if os.Getenv("TMUX") != "" {
		cmd := exec.Command(tmuxPath, "-S", tmuxSocket, "switch-client", "-t", target)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	cmd := exec.Command(tmuxPath, "-S", tmuxSocket, "attach-session", "-t", target)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Transcribe audio file using configured command or fallback to whisper
func transcribeAudio(config *Config, audioPath string) (string, error) {
	// Use configured transcription command if set
	if config.TranscriptionCmd != "" {
		cmdPath := expandPath(config.TranscriptionCmd)
		cmd := exec.Command(cmdPath, audioPath)
		output, err := cmd.Output()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return "", fmt.Errorf("%s: %s", err, string(exitErr.Stderr))
			}
			return "", err
		}
		return strings.TrimSpace(string(output)), nil
	}

	// Fallback: try to find whisper in PATH or known locations
	whisperPath := "whisper"
	if _, err := exec.LookPath("whisper"); err != nil {
		// Try common locations
		for _, p := range []string{"/opt/homebrew/bin/whisper", "/usr/local/bin/whisper"} {
			if _, err := os.Stat(p); err == nil {
				whisperPath = p
				break
			}
		}
	}

	cmd := exec.Command(whisperPath, audioPath, "--model", "small", "--output_format", "txt", "--output_dir", filepath.Dir(audioPath))
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("whisper failed: %w (set transcription_cmd in config for custom transcription)", err)
	}

	// Read the transcription
	txtPath := strings.TrimSuffix(audioPath, filepath.Ext(audioPath)) + ".txt"
	content, err := os.ReadFile(txtPath)
	if err != nil {
		return "", err
	}

	// Cleanup
	os.Remove(txtPath)

	return strings.TrimSpace(string(content)), nil
}
