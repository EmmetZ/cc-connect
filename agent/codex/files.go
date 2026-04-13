package codex

import (
	"os"
	"path/filepath"
	"strings"
)

func codexHomeDir() (string, error) {
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
		return home, nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".codex"), nil
}

func codexSessionsDir() (string, error) {
	home, err := codexHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "sessions"), nil
}

func findRolloutFile(sessionID string) string {
	if strings.TrimSpace(sessionID) == "" {
		return ""
	}

	sessionsDir, err := codexSessionsDir()
	if err != nil {
		return ""
	}

	var found string
	_ = filepath.Walk(sessionsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || found != "" {
			return nil
		}

		base := filepath.Base(path)
		if matchesRolloutFile(base, sessionID) {
			found = path
		}
		return nil
	})
	return found
}

func matchesRolloutFile(base, sessionID string) bool {
	if !strings.HasPrefix(base, "rollout-") || !strings.HasSuffix(base, ".jsonl") {
		return false
	}

	name := strings.TrimSuffix(strings.TrimPrefix(base, "rollout-"), ".jsonl")
	return name == sessionID || strings.HasSuffix(name, "-"+sessionID)
}
