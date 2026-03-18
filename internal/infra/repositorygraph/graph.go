package repositorygraph

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/infra/hclresolver"
	"gopkg.in/yaml.v3"
)

type Node struct {
	Path  string
	Kind  string
	Edges map[string]string
}

type Graph struct {
	RepoPath string
	Nodes    map[string]*Node
}

type repoFile struct {
	Rel     string
	Content []byte
}

func Build(repoPath string) (*Graph, error) {
	files, err := scan(repoPath)
	if err != nil {
		return nil, err
	}
	index := map[string]repoFile{}
	g := &Graph{RepoPath: repoPath, Nodes: map[string]*Node{}}
	for _, file := range files {
		index[file.Rel] = file
		g.ensureNode(file.Rel, classifyFile(file.Rel))
	}

	hcl := hclresolver.New(repoPath)
	for _, file := range files {
		g.addIntrinsicEdges(index, file)
		if isHCL(file.Rel) {
			refs, err := hcl.DiscoverRefs(file.Rel)
			if err == nil {
				for _, ref := range refs {
					g.linkExpanded(file.Rel, ref, "hcl-ref", index, 12)
				}
			}
		}
		if isYAML(file.Rel) {
			g.addYAMLEdges(index, file)
		}
		g.addCommandEdges(index, file)
	}

	return g, nil
}

func (g *Graph) Render(limit int) []string {
	if limit <= 0 {
		limit = 12
	}
	keys := make([]string, 0, len(g.Nodes))
	for key := range g.Nodes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > limit {
		keys = keys[:limit]
	}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		node := g.Nodes[key]
		edges := make([]string, 0, len(node.Edges))
		for edge, reason := range node.Edges {
			edges = append(edges, fmt.Sprintf("%s (%s)", edge, reason))
		}
		sort.Strings(edges)
		if len(edges) > 4 {
			edges = edges[:4]
		}
		if len(edges) == 0 {
			out = append(out, fmt.Sprintf("%s %s", node.Kind, node.Path))
			continue
		}
		out = append(out, fmt.Sprintf("%s %s -> %s", node.Kind, node.Path, strings.Join(edges, ", ")))
	}
	return out
}

func (g *Graph) Expand(seeds []string, maxDepth, limit int) map[string]int {
	if maxDepth <= 0 {
		maxDepth = 2
	}
	if limit <= 0 {
		limit = 80
	}
	type item struct {
		path  string
		depth int
	}
	queue := make([]item, 0, len(seeds))
	seenDepth := map[string]int{}
	score := map[string]int{}
	for _, seed := range seeds {
		seed = filepath.ToSlash(filepath.Clean(seed))
		if seed == "" {
			continue
		}
		queue = append(queue, item{path: seed, depth: 0})
		seenDepth[seed] = 0
	}
	for len(queue) > 0 && len(score) < limit {
		cur := queue[0]
		queue = queue[1:]
		node := g.Nodes[cur.path]
		if node == nil {
			continue
		}
		for edge := range node.Edges {
			nextDepth := cur.depth + 1
			if nextDepth > maxDepth {
				continue
			}
			bonus := 18 - nextDepth*4
			if bonus < 4 {
				bonus = 4
			}
			score[edge] += bonus
			if prev, ok := seenDepth[edge]; ok && prev <= nextDepth {
				continue
			}
			seenDepth[edge] = nextDepth
			queue = append(queue, item{path: edge, depth: nextDepth})
		}
	}
	return score
}

func (g *Graph) addIntrinsicEdges(index map[string]repoFile, file repoFile) {
	rel := file.Rel
	dir := filepath.ToSlash(filepath.Dir(rel))
	base := filepath.Base(rel)
	switch {
	case base == "Chart.yaml" || base == "Chart.yml":
		for path := range index {
			norm := filepath.ToSlash(path)
			if norm == rel {
				continue
			}
			if strings.HasPrefix(norm, dir+"/templates/") || strings.HasPrefix(filepath.Base(norm), "values") && strings.HasPrefix(norm, dir+"/") {
				g.addEdge(rel, path, "helm-chart")
			}
		}
	case base == "helmfile.yaml" || base == "helmfile.yml":
		for path := range index {
			norm := filepath.ToSlash(path)
			if norm == rel {
				continue
			}
			if strings.HasPrefix(filepath.Base(norm), "values") && (strings.HasSuffix(norm, ".yaml") || strings.HasSuffix(norm, ".yml")) {
				g.addEdge(rel, path, "helmfile-values")
			}
			if filepath.Base(norm) == "Chart.yaml" || filepath.Base(norm) == "Chart.yml" {
				g.addEdge(rel, path, "helmfile-chart")
			}
		}
	case strings.HasPrefix(base, "values") && isYAML(rel):
		for path := range index {
			norm := filepath.ToSlash(path)
			if strings.HasPrefix(norm, dir+"/templates/") || filepath.Base(norm) == "Chart.yaml" || filepath.Base(norm) == "Chart.yml" {
				g.addEdge(rel, path, "helm-values")
			}
		}
	case strings.Contains(filepath.ToSlash(rel), "/templates/"):
		for path := range index {
			norm := filepath.ToSlash(path)
			if filepath.Dir(norm) == dir || strings.HasPrefix(filepath.Base(norm), "values") && filepath.ToSlash(filepath.Dir(norm)) == filepath.ToSlash(filepath.Dir(dir)) {
				g.addEdge(rel, path, "helm-template-neighbor")
			}
		}
	case strings.EqualFold(base, "kustomization.yaml") || strings.EqualFold(base, "kustomization.yml"):
		for path := range index {
			if filepath.ToSlash(path) == rel {
				continue
			}
			if filepath.ToSlash(filepath.Dir(path)) == dir {
				g.addEdge(rel, path, "kustomize-dir")
			}
		}
	}
}

func (g *Graph) addCommandEdges(index map[string]repoFile, file repoFile) {
	for _, raw := range collectCommandRefs(string(file.Content)) {
		g.linkExpanded(file.Rel, raw, "command-ref", index, 12)
	}
}

func (g *Graph) addYAMLEdges(index map[string]repoFile, file repoFile) {
	decoder := yaml.NewDecoder(strings.NewReader(string(file.Content)))
	for {
		var doc map[string]any
		if err := decoder.Decode(&doc); err != nil {
			break
		}
		if len(doc) == 0 {
			continue
		}
		for _, raw := range collectYAMLRefs(doc) {
			g.linkExpanded(file.Rel, raw, "yaml-ref", index, 10)
		}
	}
}

func collectYAMLRefs(v any) []string {
	seen := map[string]struct{}{}
	var out []string
	var walk func(any, string)
	walk = func(node any, key string) {
		switch x := node.(type) {
		case map[string]any:
			for k, v := range x {
				walk(v, strings.ToLower(k))
			}
		case []any:
			for _, v := range x {
				walk(v, key)
			}
		case string:
			if looksLikeYAMLRefKey(key) || looksLikeYAMLRefValue(x) {
				if _, ok := seen[x]; !ok {
					seen[x] = struct{}{}
					out = append(out, x)
				}
			}
		}
	}
	walk(v, "")
	sort.Strings(out)
	return out
}

func looksLikeYAMLRefKey(key string) bool {
	return key == "path" ||
		key == "file" ||
		key == "files" ||
		key == "chart" ||
		key == "helmfiles" ||
		key == "valuesfile" ||
		key == "valuesfiles" ||
		key == "additionalvaluesfiles" ||
		key == "patches" ||
		key == "patchesstrategicmerge" ||
		key == "resources" ||
		key == "bases" ||
		key == "components"
}

func looksLikeYAMLRefValue(v string) bool {
	v = strings.TrimSpace(v)
	return strings.HasPrefix(v, "./") || strings.HasPrefix(v, "../") || strings.HasSuffix(v, ".yaml") || strings.HasSuffix(v, ".yml") || strings.HasSuffix(v, ".tpl")
}

func collectCommandRefs(content string) []string {
	seen := map[string]struct{}{}
	var out []string
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?:^|\s)(?:kubectl|oc)\s+.*?(?:-f|--filename)\s+([^\s\\]+)`),
		regexp.MustCompile(`(?:^|\s)(?:kubectl|oc)\s+.*?(?:-k|--kustomize)\s+([^\s\\]+)`),
		regexp.MustCompile(`(?:^|\s)kustomize\s+build\s+([^\s\\]+)`),
		regexp.MustCompile(`(?:^|\s)helm(?:file)?\s+.*?(?:-f|--values|--state-values-file|--file)\s+([^\s\\]+)`),
		regexp.MustCompile(`(?:^|\s)helm\s+.*?\s(\.?\.?/[^\s\\]+|[^\s\\]*charts/[^\s\\]+|[^\s\\]*templates/[^\s\\]+)`),
		regexp.MustCompile(`(?:^|\s)(?:terraform|tofu)\s+.*?-chdir=([^\s\\]+)`),
		regexp.MustCompile(`(?:^|\s)terragrunt\s+.*?--terragrunt-working-dir\s+([^\s\\]+)`),
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, re := range patterns {
			for _, match := range re.FindAllStringSubmatch(line, -1) {
				if len(match) < 2 {
					continue
				}
				raw := strings.Trim(strings.TrimSpace(match[1]), `"'`)
				raw = strings.TrimSuffix(raw, ",")
				raw = strings.TrimSuffix(raw, ";")
				if raw == "" {
					continue
				}
				if _, ok := seen[raw]; ok {
					continue
				}
				seen[raw] = struct{}{}
				out = append(out, raw)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (g *Graph) linkExpanded(from, raw, reason string, index map[string]repoFile, limit int) {
	baseDir := filepath.Dir(from)
	if baseDir == "" {
		baseDir = "."
	}
	for _, target := range resolveLocalTargets(raw, baseDir, index, limit) {
		g.addEdge(from, target, reason)
	}
}

func (g *Graph) addEdge(from, to, reason string) {
	if to == "" || from == to {
		return
	}
	node := g.ensureNode(from, classifyFile(from))
	g.ensureNode(to, classifyFile(to))
	node.Edges[to] = reason
}

func (g *Graph) ensureNode(path, kind string) *Node {
	path = filepath.ToSlash(filepath.Clean(path))
	if path == "." {
		path = filepath.ToSlash(path)
	}
	if node, ok := g.Nodes[path]; ok {
		if node.Kind == "" && kind != "" {
			node.Kind = kind
		}
		return node
	}
	node := &Node{Path: path, Kind: kind, Edges: map[string]string{}}
	g.Nodes[path] = node
	return node
}

func resolveLocalTargets(raw, baseDir string, index map[string]repoFile, limit int) []string {
	raw = normalizeRef(raw)
	if raw == "" || isExternal(raw) {
		return nil
	}
	candidates := []string{filepath.ToSlash(filepath.Clean(filepath.Join(baseDir, raw)))}
	if !strings.HasPrefix(raw, ".") && !strings.HasPrefix(raw, "..") && !filepath.IsAbs(raw) {
		candidates = append(candidates, filepath.Clean(raw))
	}
	seen := map[string]struct{}{}
	var out []string
	for _, candidate := range candidates {
		candidate = filepath.ToSlash(filepath.Clean(candidate))
		for _, match := range matchCandidate(candidate, index, limit) {
			if _, ok := seen[match]; ok {
				continue
			}
			seen[match] = struct{}{}
			out = append(out, match)
		}
	}
	sort.Strings(out)
	return out
}

func matchCandidate(candidate string, index map[string]repoFile, limit int) []string {
	if candidate == "" || candidate == "." {
		return nil
	}
	if _, ok := index[candidate]; ok {
		return expandTarget(candidate, index, limit)
	}
	prefix := strings.TrimSuffix(candidate, "/")
	var out []string
	for rel := range index {
		norm := filepath.ToSlash(rel)
		if norm == prefix || strings.HasPrefix(norm, prefix+"/") {
			out = append(out, rel)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := targetPriority(out[i]), targetPriority(out[j])
		if pi == pj {
			return out[i] < out[j]
		}
		return pi > pj
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func expandTarget(rel string, index map[string]repoFile, limit int) []string {
	dir := filepath.ToSlash(filepath.Dir(rel))
	out := []string{rel}
	if classifyFile(rel) == "helm-chart" || strings.Contains(filepath.ToSlash(rel), "/templates/") || strings.HasPrefix(filepath.Base(rel), "values") {
		for path := range index {
			norm := filepath.ToSlash(path)
			if strings.HasPrefix(norm, dir+"/templates/") || filepath.Dir(norm) == dir {
				out = append(out, path)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := targetPriority(out[i]), targetPriority(out[j])
		if pi == pj {
			return out[i] < out[j]
		}
		return pi > pj
	})
	dedup := out[:0]
	seen := map[string]struct{}{}
	for _, item := range out {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		dedup = append(dedup, item)
	}
	if limit > 0 && len(dedup) > limit {
		dedup = dedup[:limit]
	}
	return dedup
}

func targetPriority(rel string) int {
	base := filepath.Base(rel)
	switch {
	case base == "Chart.yaml" || base == "Chart.yml":
		return 100
	case base == "helmfile.yaml" || base == "helmfile.yml":
		return 97
	case strings.HasPrefix(base, "values-") && isYAML(rel):
		return 95
	case base == "values.yaml" || base == "values.yml":
		return 90
	case strings.Contains(filepath.ToSlash(rel), "/templates/deployment"):
		return 88
	case strings.Contains(filepath.ToSlash(rel), "/templates/statefulset"), strings.Contains(filepath.ToSlash(rel), "/templates/sts"):
		return 87
	case strings.Contains(filepath.ToSlash(rel), "/templates/service"):
		return 86
	case strings.Contains(filepath.ToSlash(rel), "/templates/ingress"):
		return 85
	case strings.Contains(filepath.ToSlash(rel), "/templates/hpa"):
		return 84
	case strings.EqualFold(base, "kustomization.yaml"), strings.EqualFold(base, "kustomization.yml"):
		return 83
	case isCommandFile(rel):
		return 78
	case isYAML(rel):
		return 70
	case isHCL(rel):
		return 60
	default:
		return 10
	}
}

func classifyFile(rel string) string {
	base := filepath.Base(rel)
	switch {
	case isHCL(rel):
		return "hcl"
	case base == "Chart.yaml" || base == "Chart.yml":
		return "helm-chart"
	case base == "helmfile.yaml" || base == "helmfile.yml":
		return "helmfile"
	case strings.HasPrefix(base, "values") && isYAML(rel):
		return "helm-values"
	case strings.Contains(filepath.ToSlash(rel), "/templates/"):
		return "helm-template"
	case strings.EqualFold(base, "kustomization.yaml"), strings.EqualFold(base, "kustomization.yml"):
		return "kustomization"
	case isCommandFile(rel):
		return "command-entrypoint"
	case isYAML(rel):
		return "manifest"
	default:
		return "file"
	}
}

func normalizeRef(raw string) string {
	raw = strings.TrimSpace(raw)
	replacements := map[string]string{
		"${get_original_terragrunt_dir()}": ".",
		"${get_terragrunt_dir()}":          ".",
		"${path.module}":                   ".",
	}
	for from, to := range replacements {
		raw = strings.ReplaceAll(raw, from, to)
	}
	raw = strings.ReplaceAll(raw, `\`, `/`)
	raw = strings.ReplaceAll(raw, "//", "/")
	return filepath.ToSlash(filepath.Clean(raw))
}

func isExternal(raw string) bool {
	return strings.Contains(raw, "://") || strings.HasPrefix(raw, "git::") || strings.HasPrefix(raw, "git@") || strings.HasPrefix(raw, "oci://")
}

func scan(repoPath string) ([]repoFile, error) {
	var files []repoFile
	err := filepath.WalkDir(repoPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".terraform", ".terragrunt-cache":
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(repoPath, path)
		if err != nil {
			return err
		}
		if !supported(rel) {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		files = append(files, repoFile{Rel: rel, Content: content})
		return nil
	})
	return files, err
}

func supported(rel string) bool {
	ext := strings.ToLower(filepath.Ext(rel))
	base := filepath.Base(rel)
	switch ext {
	case ".tf", ".tfvars", ".hcl", ".yaml", ".yml", ".tpl", ".json", ".sh", ".bash", ".zsh":
		return true
	}
	switch base {
	case "Chart.yaml", "Chart.yml", "helmfile.yaml", "helmfile.yml", "kustomization.yaml", "kustomization.yml", "Makefile", "GNUmakefile", "Justfile", "justfile", "Tiltfile":
		return true
	}
	return false
}

func isHCL(rel string) bool {
	ext := strings.ToLower(filepath.Ext(rel))
	return ext == ".tf" || ext == ".tfvars" || ext == ".hcl"
}

func isYAML(rel string) bool {
	ext := strings.ToLower(filepath.Ext(rel))
	return ext == ".yaml" || ext == ".yml"
}

func isCommandFile(rel string) bool {
	base := filepath.Base(rel)
	ext := strings.ToLower(filepath.Ext(rel))
	switch ext {
	case ".sh", ".bash", ".zsh":
		return true
	}
	switch base {
	case "Makefile", "GNUmakefile", "Justfile", "justfile", "Tiltfile":
		return true
	default:
		return false
	}
}
