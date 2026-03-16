package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolvePlanPath attempts to make common invocations more forgiving.
// If the provided path doesn't exist and looks like a bare filename, it also tries ./examples/<name>.
func resolvePlanPath(planPath string) (string, error) {
	p := strings.TrimSpace(planPath)
	if p == "" {
		return "", fmt.Errorf("missing --plan")
	}
	if fileExists(p) {
		return p, nil
	}

	// If caller passed only a filename (no directory), try resolving from ./examples.
	if !strings.ContainsRune(p, filepath.Separator) {
		candidate := filepath.Join("examples", p)
		if fileExists(candidate) {
			return candidate, nil
		}
	}

	wd, _ := os.Getwd()
	return "", fmt.Errorf("plan file not found: %s (cwd=%s)", p, wd)
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !st.IsDir()
}
