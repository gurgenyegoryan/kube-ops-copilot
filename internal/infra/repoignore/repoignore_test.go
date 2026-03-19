package repoignore

import "testing"

func TestShouldSkipDir(t *testing.T) {
	cases := map[string]bool{
		".git":                true,
		".terraform":          true,
		".terraform.d":        true,
		".terragrunt-cache":   true,
		".terragrunt-cache-x": true,
		".terragrunt-download": true,
		".helm":               true,
		".helm-cache":         true,
		".helm-cache-v2":      true,
		".chartcache":         true,
		".github":             false,
		"helm-charts":         false,
		"charts":              false,
		"templates":           false,
		"apps":                false,
	}
	for name, want := range cases {
		if got := ShouldSkipDir(name); got != want {
			t.Fatalf("ShouldSkipDir(%q) = %t, want %t", name, got, want)
		}
	}
}
