package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func buildTerraformRepoInventory(repoPath string, maxFiles int, maxBytes int) (string, error) {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return "", fmt.Errorf("missing repo path")
	}
	var files []string
	err := filepath.WalkDir(repoPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == ".terraform" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".tf" || ext == ".tfvars" || ext == ".hcl" {
			rel, relErr := filepath.Rel(repoPath, path)
			if relErr != nil {
				return relErr
			}
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	if maxFiles <= 0 {
		maxFiles = 20
	}
	if len(files) > maxFiles {
		files = files[:maxFiles]
	}
	if maxBytes <= 0 {
		maxBytes = 50000
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Terraform repo path: %s\n", repoPath)
	fmt.Fprintf(&b, "Candidate Terraform files (sampled):\n")
	for _, f := range files {
		fmt.Fprintf(&b, "- %s\n", f)
	}
	fmt.Fprintf(&b, "\nFile contents:\n")
	remaining := maxBytes
	for _, rel := range files {
		if remaining <= 0 {
			break
		}
		full := filepath.Join(repoPath, rel)
		content, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		if len(content) > remaining {
			content = content[:remaining]
		}
		text := string(content)
		fmt.Fprintf(&b, "\n--- FILE: %s ---\n%s\n", rel, text)
		remaining -= len(content)
	}
	return b.String(), nil
}
