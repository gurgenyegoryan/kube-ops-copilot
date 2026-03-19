package repoignore

import "strings"

// ShouldSkipDir reports whether a directory is an infra/cache/generated folder
// that should not participate in repo inventory or repository graph traversal.
func ShouldSkipDir(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	switch name {
	case ".git",
		".terraform",
		".terraform.d",
		".terragrunt-cache",
		".terragrunt-download",
		".helm",
		".helm-cache",
		".chartcache":
		return true
	}
	if strings.HasPrefix(name, ".terragrunt-cache") {
		return true
	}
	if strings.HasPrefix(name, ".terraform") && name != ".github" {
		return true
	}
	if strings.HasPrefix(name, ".helm-cache") {
		return true
	}
	return false
}
