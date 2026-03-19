package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func writeTextArtifact(prefix, content string) (string, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "kube-ops-copilot-artifact"
	}
	filename := fmt.Sprintf("%s-%s.txt", prefix, time.Now().UTC().Format("20060102T150405Z"))
	path := filepath.Join(os.TempDir(), filename)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
