package cli

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/infra/hclresolver"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/infra/repositorygraph"
)

type repoFile struct {
	rel     string
	full    string
	content string
}

type inventoryCandidate struct {
	rel     string
	score   int
	preview string
}

type semanticHints struct {
	Namespaces []string
	Workloads  []string
	Aliases    []string
	Envs       []string
}

func buildTerraformRepoInventory(repoPath string, hintText string, maxFiles int, maxBytes int) (string, error) {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return "", fmt.Errorf("missing repo path")
	}
	keywords := extractHintKeywords(hintText)
	semantic := extractSemanticHints(hintText)
	repoFiles, err := scanInfraRepoFiles(repoPath)
	if err != nil {
		return "", err
	}
	fileIndex := map[string]repoFile{}
	for _, file := range repoFiles {
		fileIndex[file.rel] = file
	}
	resolver := hclresolver.New(repoPath)
	repoGraph, _ := repositorygraph.Build(repoPath)
	graph, _ := buildModuleGraph(repoPath)
	candidates := map[string]*inventoryCandidate{}
	for _, file := range repoFiles {
		score, preview := scoreRepoFile(file, keywords, semantic, graph)
		candidates[file.rel] = &inventoryCandidate{rel: file.rel, score: score, preview: preview}
	}

	expandedLinks := map[string]int{}
	for rel, bonus := range semanticCandidateBonuses(repoFiles, semantic) {
		expandedLinks[rel] += bonus
	}
	seedFiles := topCandidateRels(candidates, minInt(maxFiles, 12))
	if repoGraph != nil {
		for rel, bonus := range repoGraph.Expand(seedFiles, 3, 160) {
			expandedLinks[rel] += bonus
		}
	}
	for _, rel := range seedFiles {
		file, ok := fileIndex[rel]
		if !ok {
			continue
		}
		for _, linked := range extractLocalReferenceTargets(repoPath, file, fileIndex, resolver) {
			expandedLinks[linked] += 18
		}
	}
	for rel, bonus := range expandedLinks {
		candidate, ok := candidates[rel]
		if !ok {
			file, exists := fileIndex[rel]
			if !exists {
				continue
			}
			score, preview := scoreRepoFile(file, keywords, semantic, graph)
			candidate = &inventoryCandidate{rel: rel, score: score, preview: preview}
			candidates[rel] = candidate
		}
		candidate.score += bonus
	}

	files := make([]inventoryCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		files = append(files, *candidate)
	}
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].score == files[j].score {
			return files[i].rel < files[j].rel
		}
		return files[i].score > files[j].score
	})
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
	fmt.Fprintf(&b, "Infrastructure repo path: %s\n", repoPath)
	fmt.Fprintf(&b, "Repo narrowing hints: %s\n", strings.Join(keywords, ", "))
	if len(semantic.Workloads) > 0 || len(semantic.Namespaces) > 0 || len(semantic.Envs) > 0 {
		fmt.Fprintf(&b, "Semantic cluster targets: workloads=%s namespaces=%s envs=%s aliases=%s\n",
			joinListOrNone(semantic.Workloads),
			joinListOrNone(semantic.Namespaces),
			joinListOrNone(semantic.Envs),
			joinListOrNone(limitStrings(semantic.Aliases, 12)),
		)
	}
	if len(graph.Nodes) > 0 {
		fmt.Fprintf(&b, "Detected module/dependency graph:\n")
		for _, line := range graph.Render(12) {
			fmt.Fprintf(&b, "- %s\n", line)
		}
	}
	if repoGraph != nil && len(repoGraph.Nodes) > 0 {
		fmt.Fprintf(&b, "Detected infrastructure repository graph:\n")
		for _, line := range repoGraph.Render(12) {
			fmt.Fprintf(&b, "- %s\n", line)
		}
	}
	if len(expandedLinks) > 0 {
		fmt.Fprintf(&b, "Discovered linked infra files from local source/path/chart/value references:\n")
		for _, rel := range sortedLinkKeys(expandedLinks, 16) {
			fmt.Fprintf(&b, "- %s (link-score=%d)\n", rel, expandedLinks[rel])
		}
	}
	fmt.Fprintf(&b, "Candidate infra files (sampled and scored):\n")
	for _, f := range files {
		fmt.Fprintf(&b, "- %s (score=%d)\n", f.rel, f.score)
	}
	fmt.Fprintf(&b, "\nFile contents:\n")
	remaining := maxBytes
	for _, item := range files {
		if remaining <= 0 {
			break
		}
		content := []byte(item.preview)
		if len(content) > remaining {
			content = content[:remaining]
		}
		text := string(content)
		fmt.Fprintf(&b, "\n--- FILE: %s ---\n%s\n", item.rel, text)
		remaining -= len(content)
	}
	return b.String(), nil
}

func scanInfraRepoFiles(repoPath string) ([]repoFile, error) {
	var files []repoFile
	err := filepath.WalkDir(repoPath, func(current string, d os.DirEntry, err error) error {
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
		rel, relErr := filepath.Rel(repoPath, current)
		if relErr != nil {
			return relErr
		}
		if !isSupportedInfraFile(rel) {
			return nil
		}
		content, readErr := os.ReadFile(current)
		if readErr != nil {
			return nil
		}
		files = append(files, repoFile{rel: rel, full: current, content: string(content)})
		return nil
	})
	return files, err
}

func isSupportedInfraFile(rel string) bool {
	base := filepath.Base(rel)
	ext := strings.ToLower(filepath.Ext(rel))
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

func topCandidateRels(candidates map[string]*inventoryCandidate, limit int) []string {
	if limit <= 0 {
		limit = 8
	}
	files := make([]inventoryCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		files = append(files, *candidate)
	}
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].score == files[j].score {
			return files[i].rel < files[j].rel
		}
		return files[i].score > files[j].score
	})
	if len(files) > limit {
		files = files[:limit]
	}
	out := make([]string, 0, len(files))
	for _, file := range files {
		out = append(out, file.rel)
	}
	return out
}

type moduleGraph struct {
	Nodes map[string]moduleNode
}

type moduleNode struct {
	Path  string
	Name  string
	Kind  string
	Edges []string
}

func (g moduleGraph) Render(limit int) []string {
	if limit <= 0 {
		limit = 12
	}
	keys := make([]string, 0, len(g.Nodes))
	for k := range g.Nodes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > limit {
		keys = keys[:limit]
	}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		node := g.Nodes[key]
		edges := append([]string(nil), node.Edges...)
		sort.Strings(edges)
		if len(edges) > 4 {
			edges = edges[:4]
		}
		if len(edges) == 0 {
			out = append(out, fmt.Sprintf("%s %s (%s)", node.Kind, node.Name, node.Path))
			continue
		}
		out = append(out, fmt.Sprintf("%s %s (%s) -> %s", node.Kind, node.Name, node.Path, strings.Join(edges, ", ")))
	}
	return out
}

func buildModuleGraph(repoPath string) (moduleGraph, error) {
	g := moduleGraph{Nodes: map[string]moduleNode{}}

	err := filepath.WalkDir(repoPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == ".terraform" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".tf" && ext != ".hcl" && ext != ".tfvars" {
			return nil
		}
		rel, err := filepath.Rel(repoPath, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		text := string(content)
		dir := filepath.Dir(rel)
		if dir == "" {
			dir = "."
		}
		addStackNode(&g, dir, rel)

		for _, block := range findNamedBlocks(text, "module") {
			key := rel + "#module:" + block.Name
			node := moduleNode{Path: rel, Name: block.Name, Kind: "module"}
			if src := extractStringAttribute(block.Body, "source"); src != "" {
				node.Edges = append(node.Edges, renderResolvedEdge(dir, src))
			}
			g.Nodes[key] = node
		}
		for _, block := range findNamedBlocks(text, "dependency") {
			key := rel + "#dependency:" + block.Name
			node := moduleNode{Path: rel, Name: block.Name, Kind: "dependency"}
			if src := extractStringAttribute(block.Body, "config_path"); src != "" {
				node.Edges = append(node.Edges, renderResolvedEdge(dir, src))
			}
			g.Nodes[key] = node
		}
		for _, block := range findNamedBlocks(text, "include") {
			key := rel + "#include:" + block.Name
			node := moduleNode{Path: rel, Name: block.Name, Kind: "include"}
			if src := extractStringAttribute(block.Body, "path"); src != "" {
				node.Edges = append(node.Edges, renderResolvedEdge(dir, src))
			}
			if src := extractStringAttribute(block.Body, "config_path"); src != "" {
				node.Edges = append(node.Edges, renderResolvedEdge(dir, src))
			}
			g.Nodes[key] = node
		}
		for _, block := range findAnonymousBlocks(text, "terraform") {
			src := extractStringAttribute(block.Body, "source")
			if src == "" {
				continue
			}
			key := "dir:" + dir
			node := g.Nodes[key]
			node.Edges = append(node.Edges, renderResolvedEdge(dir, src))
			g.Nodes[key] = node
		}
		return nil
	})
	return g, err
}

type namedBlock struct {
	Name string
	Body string
}

func addStackNode(g *moduleGraph, dir, rel string) {
	key := "dir:" + dir
	if _, ok := g.Nodes[key]; ok {
		return
	}
	name := filepath.Base(dir)
	if dir == "." {
		name = "root"
	}
	g.Nodes[key] = moduleNode{
		Path: dir,
		Name: name,
		Kind: "stack",
	}
}

func findNamedBlocks(text, blockType string) []namedBlock {
	re := regexp.MustCompile(`(?m)` + regexp.QuoteMeta(blockType) + `\s+"([^"]+)"\s*\{`)
	var out []namedBlock
	for _, loc := range re.FindAllStringSubmatchIndex(text, -1) {
		if len(loc) < 4 {
			continue
		}
		openIdx := loc[1] - 1
		end, ok := scanBlockEnd(text, openIdx)
		if !ok {
			continue
		}
		out = append(out, namedBlock{
			Name: text[loc[2]:loc[3]],
			Body: text[loc[0]:end],
		})
	}
	return out
}

func findAnonymousBlocks(text, blockType string) []namedBlock {
	re := regexp.MustCompile(`(?m)` + regexp.QuoteMeta(blockType) + `\s*\{`)
	var out []namedBlock
	for _, loc := range re.FindAllStringIndex(text, -1) {
		if len(loc) < 2 {
			continue
		}
		openIdx := loc[1] - 1
		end, ok := scanBlockEnd(text, openIdx)
		if !ok {
			continue
		}
		out = append(out, namedBlock{
			Name: blockType,
			Body: text[loc[0]:end],
		})
	}
	return out
}

func scanBlockEnd(text string, openIdx int) (int, bool) {
	depth := 0
	inString := false
	escaped := false
	for i := openIdx; i < len(text); i++ {
		ch := text[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		if ch == '"' {
			inString = true
			continue
		}
		switch ch {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

func extractStringAttribute(body, name string) string {
	re := regexp.MustCompile(`(?m)` + regexp.QuoteMeta(name) + `\s*=\s*"([^"]+)"`)
	match := re.FindStringSubmatch(body)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSpace(match[1])
}

func renderResolvedEdge(baseDir, raw string) string {
	resolved := resolveRelativeRef(baseDir, raw)
	if resolved == "" || resolved == raw {
		return raw
	}
	return raw + " => " + resolved
}

func resolveRelativeRef(baseDir, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") || strings.HasPrefix(raw, "git::") || strings.HasPrefix(raw, "git@") || strings.HasPrefix(raw, "tfr://") {
		return raw
	}
	if !strings.HasPrefix(raw, ".") && !strings.HasPrefix(raw, "..") {
		return raw
	}
	dir := filepath.Clean(filepath.Join(baseDir, raw))
	if dir == "" {
		return "."
	}
	return dir
}

func extractHintKeywords(hintText string) []string {
	normalized := strings.NewReplacer("\n", " ", "\t", " ", "/", " ", "-", " ", "_", " ", ".", " ", ",", " ", ":", " ").Replace(strings.ToLower(hintText))
	seen := map[string]struct{}{}
	var out []string
	for _, token := range strings.Fields(normalized) {
		if len(token) < 4 {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, token)
		if len(out) >= 20 {
			break
		}
	}
	sort.Strings(out)
	return out
}

func scoreRepoFile(file repoFile, keywords []string, semantic semanticHints, graph moduleGraph) (int, string) {
	score := 0
	lowerRel := strings.ToLower(file.rel)
	for _, keyword := range keywords {
		if strings.Contains(lowerRel, keyword) {
			score += 5
		}
	}
	preview := file.content
	if len(preview) > 8000 {
		preview = preview[:8000]
	}
	lowerPreview := strings.ToLower(preview)
	for _, keyword := range keywords {
		if strings.Contains(lowerPreview, keyword) {
			score += 2
		}
	}
	if looksLikeChartFile(file.rel) {
		score += 8
	}
	if looksLikeCommandFile(file.rel) {
		score += 6
	}
	if looksLikeHelmfile(file.rel) {
		score += 7
	}
	score += semanticScoreFile(file, semantic)
	for _, node := range graph.Nodes {
		if node.Path == file.rel || filepath.Dir(file.rel) == node.Path {
			score += 4
		}
		for _, keyword := range keywords {
			if strings.Contains(strings.ToLower(node.Name), keyword) {
				score += 6
			}
			for _, edge := range node.Edges {
				if strings.Contains(strings.ToLower(edge), keyword) {
					score += 3
				}
				if strings.Contains(edge, file.rel) || strings.Contains(edge, filepath.Dir(file.rel)) {
					score += 2
				}
			}
		}
	}
	return score, preview
}

func semanticScoreFile(file repoFile, semantic semanticHints) int {
	if len(semantic.Workloads) == 0 && len(semantic.Aliases) == 0 && len(semantic.Envs) == 0 && len(semantic.Namespaces) == 0 {
		return 0
	}
	score := 0
	lowerRel := strings.ToLower(file.rel)
	lowerContent := strings.ToLower(file.content)
	if len(lowerContent) > 4000 {
		lowerContent = lowerContent[:4000]
	}
	relTokens := tokenizeIdentifier(lowerRel)

	for _, workload := range semantic.Workloads {
		if strings.Contains(lowerRel, workload) {
			score += 18
		}
	}
	for _, alias := range semantic.Aliases {
		if alias == "" {
			continue
		}
		if strings.Contains(lowerRel, alias) {
			score += 12
		}
		if strings.Contains(lowerContent, alias) {
			score += 5
		}
	}
	for _, env := range semantic.Envs {
		if env == "" {
			continue
		}
		if strings.Contains(lowerRel, "/"+env+"/") || strings.Contains(lowerRel, "-"+env+".yaml") || strings.Contains(lowerRel, "-"+env+".yml") {
			score += 9
		}
	}
	for _, ns := range semantic.Namespaces {
		if strings.Contains(lowerContent, ns) {
			score += 4
		}
	}

	aliasSet := map[string]struct{}{}
	for _, alias := range semantic.Aliases {
		if alias == "" {
			continue
		}
		aliasSet[alias] = struct{}{}
		for _, token := range tokenizeIdentifier(alias) {
			if len(token) >= 4 {
				aliasSet[token] = struct{}{}
			}
		}
	}
	overlap := 0
	for _, token := range relTokens {
		if _, ok := aliasSet[token]; ok {
			overlap++
		}
	}
	score += overlap * 3
	return score
}

func extractLocalReferenceTargets(repoPath string, file repoFile, index map[string]repoFile, resolver *hclresolver.Resolver) []string {
	seen := map[string]struct{}{}
	if resolver != nil && isHCLFile(file.rel) {
		if structured, err := resolver.DiscoverRefs(file.rel); err == nil {
			for _, rel := range structured {
				for _, expanded := range expandLinkedTarget(rel, index) {
					seen[expanded] = struct{}{}
				}
			}
		}
	}
	for _, raw := range extractReferenceStrings(file.content) {
		baseDir := filepath.Dir(file.rel)
		if baseDir == "" {
			baseDir = "."
		}
		for _, rel := range resolveReferenceTargets(repoPath, baseDir, raw, index) {
			if rel == file.rel {
				continue
			}
			seen[rel] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for rel := range seen {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

func extractReferenceStrings(content string) []string {
	assignmentRe := regexp.MustCompile(`(?im)([A-Za-z0-9_]*(?:path|file|source|chart|values|template)[A-Za-z0-9_]*)\s*=\s*"([^"]+)"`)
	quoteRe := regexp.MustCompile(`"([^"]+)"`)
	seen := map[string]struct{}{}
	var out []string
	for _, match := range assignmentRe.FindAllStringSubmatch(content, -1) {
		if len(match) < 3 {
			continue
		}
		raw := strings.TrimSpace(match[2])
		if raw == "" {
			continue
		}
		if _, ok := seen[raw]; ok {
			continue
		}
		seen[raw] = struct{}{}
		out = append(out, raw)
	}
	for _, line := range strings.Split(content, "\n") {
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "path") && !strings.Contains(lower, "file") && !strings.Contains(lower, "source") && !strings.Contains(lower, "chart") && !strings.Contains(lower, "values") {
			if collectCommandReferenceStrings(line, seen, &out) == 0 {
				continue
			}
		}
		for _, match := range quoteRe.FindAllStringSubmatch(line, -1) {
			if len(match) < 2 {
				continue
			}
			raw := strings.TrimSpace(match[1])
			if raw == "" {
				continue
			}
			if _, ok := seen[raw]; ok {
				continue
			}
			seen[raw] = struct{}{}
			out = append(out, raw)
		}
		collectCommandReferenceStrings(line, seen, &out)
	}
	return out
}

func collectCommandReferenceStrings(line string, seen map[string]struct{}, out *[]string) int {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return 0
	}
	before := len(*out)
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?:^|\s)(?:kubectl|oc)\s+.*?(?:-f|--filename)\s+([^\s\\]+)`),
		regexp.MustCompile(`(?:^|\s)(?:kubectl|oc)\s+.*?(?:-k|--kustomize)\s+([^\s\\]+)`),
		regexp.MustCompile(`(?:^|\s)kustomize\s+build\s+([^\s\\]+)`),
		regexp.MustCompile(`(?:^|\s)helm(?:file)?\s+.*?(?:-f|--values|--state-values-file|--file)\s+([^\s\\]+)`),
		regexp.MustCompile(`(?:^|\s)helm\s+.*?\s(\.?\.?/[^\s\\]+|[^\s\\]*charts/[^\s\\]+|[^\s\\]*templates/[^\s\\]+)`),
		regexp.MustCompile(`(?:^|\s)(?:terraform|tofu)\s+.*?-chdir=([^\s\\]+)`),
		regexp.MustCompile(`(?:^|\s)terragrunt\s+.*?--terragrunt-working-dir\s+([^\s\\]+)`),
	}
	for _, re := range patterns {
		for _, match := range re.FindAllStringSubmatch(line, -1) {
			if len(match) < 2 {
				continue
			}
			raw := cleanCommandRef(match[1])
			if raw == "" {
				continue
			}
			if _, ok := seen[raw]; ok {
				continue
			}
			seen[raw] = struct{}{}
			*out = append(*out, raw)
		}
	}
	return len(*out) - before
}

func cleanCommandRef(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.Trim(raw, `"'`)
	raw = strings.TrimSuffix(raw, ",")
	raw = strings.TrimSuffix(raw, ";")
	if raw == "" {
		return ""
	}
	return raw
}

func resolveReferenceTargets(repoPath, baseDir, raw string, index map[string]repoFile) []string {
	pattern := normalizeReferencePattern(raw)
	if pattern == "" || isExternalReference(pattern) {
		return nil
	}
	candidates := []string{}
	if filepath.IsAbs(pattern) {
		if rel, err := filepath.Rel(repoPath, pattern); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			candidates = append(candidates, rel)
		}
	} else {
		candidates = append(candidates, filepath.Clean(filepath.Join(baseDir, pattern)))
		if !strings.HasPrefix(pattern, ".") && !strings.HasPrefix(pattern, "..") {
			candidates = append(candidates, filepath.Clean(pattern))
		}
	}

	seen := map[string]struct{}{}
	var out []string
	for _, candidate := range candidates {
		for _, rel := range matchReferenceCandidate(candidate, index) {
			if _, ok := seen[rel]; ok {
				continue
			}
			seen[rel] = struct{}{}
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

func normalizeReferencePattern(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	replacements := map[string]string{
		"${get_original_terragrunt_dir()}": ".",
		"${get_terragrunt_dir()}":          ".",
		"${path.module}":                   ".",
	}
	for from, to := range replacements {
		raw = strings.ReplaceAll(raw, from, to)
	}
	interpRe := regexp.MustCompile(`\$\{[^}]+\}`)
	raw = interpRe.ReplaceAllString(raw, "*")
	if strings.Contains(raw, "://") || strings.HasPrefix(raw, "git::") || strings.HasPrefix(raw, "git@") || strings.HasPrefix(raw, "tfr://") {
		return raw
	}
	raw = strings.ReplaceAll(raw, `\`, `/`)
	raw = strings.ReplaceAll(raw, "//", "/")
	return strings.TrimSpace(raw)
}

func isExternalReference(raw string) bool {
	return strings.Contains(raw, "://") || strings.HasPrefix(raw, "git::") || strings.HasPrefix(raw, "git@") || strings.HasPrefix(raw, "tfr://")
}

func matchReferenceCandidate(candidate string, index map[string]repoFile) []string {
	candidate = filepath.ToSlash(candidate)
	if candidate == "" || candidate == "." {
		return nil
	}
	if strings.Contains(candidate, "*") {
		return matchWildcardTargets(candidate, index)
	}
	if _, ok := index[candidate]; ok {
		return expandLinkedTarget(candidate, index)
	}
	prefix := strings.TrimSuffix(candidate, "/")
	if prefix == "" {
		return nil
	}
	return expandDirectoryTarget(prefix, index)
}

func matchWildcardTargets(pattern string, index map[string]repoFile) []string {
	seen := map[string]struct{}{}
	var out []string
	for rel := range index {
		normalized := filepath.ToSlash(rel)
		ok, err := path.Match(pattern, normalized)
		if err != nil || !ok {
			continue
		}
		for _, expanded := range expandLinkedTarget(rel, index) {
			if _, exists := seen[expanded]; exists {
				continue
			}
			seen[expanded] = struct{}{}
			out = append(out, expanded)
		}
	}
	sort.Strings(out)
	return out
}

func expandLinkedTarget(rel string, index map[string]repoFile) []string {
	seen := map[string]struct{}{rel: struct{}{}}
	out := []string{rel}
	dir := filepath.Dir(rel)
	if looksLikeChartFile(rel) || strings.Contains(filepath.ToSlash(rel), "/templates/") {
		for _, linked := range expandDirectoryTarget(dir, index) {
			if _, ok := seen[linked]; ok {
				continue
			}
			seen[linked] = struct{}{}
			out = append(out, linked)
		}
	}
	sort.Strings(out)
	return out
}

func expandDirectoryTarget(dir string, index map[string]repoFile) []string {
	dir = strings.Trim(filepath.ToSlash(dir), "/")
	if dir == "" || dir == "." {
		dir = "."
	}
	var files []string
	for rel := range index {
		normalized := filepath.ToSlash(rel)
		if dir == "." {
			if strings.Count(normalized, "/") > 2 {
				continue
			}
			files = append(files, rel)
			continue
		}
		if normalized == dir || strings.HasPrefix(normalized, dir+"/") {
			files = append(files, rel)
		}
	}
	sort.SliceStable(files, func(i, j int) bool {
		si := linkedTargetPriority(files[i])
		sj := linkedTargetPriority(files[j])
		if si == sj {
			return files[i] < files[j]
		}
		return si > sj
	})
	if len(files) > 10 {
		files = files[:10]
	}
	return files
}

func linkedTargetPriority(rel string) int {
	base := filepath.Base(rel)
	switch {
	case base == "Chart.yaml" || base == "Chart.yml":
		return 100
	case base == "helmfile.yaml" || base == "helmfile.yml":
		return 97
	case strings.HasPrefix(base, "values-") && (strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml")):
		return 95
	case base == "values.yaml" || base == "values.yml":
		return 90
	case strings.Contains(filepath.ToSlash(rel), "/templates/deployment"):
		return 88
	case strings.Contains(filepath.ToSlash(rel), "/templates/statefulset") || strings.Contains(filepath.ToSlash(rel), "/templates/sts"):
		return 87
	case strings.Contains(filepath.ToSlash(rel), "/templates/service"):
		return 85
	case strings.Contains(filepath.ToSlash(rel), "/templates/ingress"):
		return 84
	case strings.Contains(filepath.ToSlash(rel), "/templates/hpa"):
		return 83
	case strings.Contains(filepath.ToSlash(rel), "/templates/pdb"):
		return 82
	case strings.HasSuffix(base, ".tpl"):
		return 80
	case looksLikeCommandFile(rel):
		return 76
	case strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml"):
		return 70
	case strings.HasSuffix(base, ".hcl") || strings.HasSuffix(base, ".tf"):
		return 60
	default:
		return 10
	}
}

func looksLikeChartFile(rel string) bool {
	base := filepath.Base(rel)
	switch {
	case base == "Chart.yaml" || base == "Chart.yml":
		return true
	case looksLikeHelmfile(rel):
		return true
	case base == "values.yaml" || base == "values.yml":
		return true
	case strings.HasPrefix(base, "values-") && (strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml")):
		return true
	case strings.Contains(filepath.ToSlash(rel), "/templates/"):
		return true
	default:
		return false
	}
}

func looksLikeHelmfile(rel string) bool {
	base := filepath.Base(rel)
	return base == "helmfile.yaml" || base == "helmfile.yml"
}

func isHCLFile(rel string) bool {
	ext := strings.ToLower(filepath.Ext(rel))
	return ext == ".hcl" || ext == ".tf" || ext == ".tfvars"
}

func looksLikeCommandFile(rel string) bool {
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

func sortedLinkKeys(values map[string]int, limit int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.SliceStable(keys, func(i, j int) bool {
		if values[keys[i]] == values[keys[j]] {
			return keys[i] < keys[j]
		}
		return values[keys[i]] > values[keys[j]]
	})
	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
	}
	return keys
}

func extractSemanticHints(hintText string) semanticHints {
	nsNameRe := regexp.MustCompile(`\b([a-z0-9][a-z0-9-]{2,})/([a-z0-9][a-z0-9-]{2,})\b`)
	nsSeen := map[string]struct{}{}
	workloadSeen := map[string]struct{}{}
	aliasSeen := map[string]struct{}{}
	envSeen := map[string]struct{}{}
	hints := semanticHints{}

	for _, match := range nsNameRe.FindAllStringSubmatch(strings.ToLower(hintText), -1) {
		if len(match) < 3 {
			continue
		}
		ns := strings.TrimSpace(match[1])
		workload := strings.TrimSpace(match[2])
		if ns != "" {
			if _, ok := nsSeen[ns]; !ok {
				nsSeen[ns] = struct{}{}
				hints.Namespaces = append(hints.Namespaces, ns)
			}
			for _, env := range inferEnvHints(ns) {
				if _, ok := envSeen[env]; ok || env == "" {
					continue
				}
				envSeen[env] = struct{}{}
				hints.Envs = append(hints.Envs, env)
			}
		}
		if workload != "" {
			if _, ok := workloadSeen[workload]; !ok {
				workloadSeen[workload] = struct{}{}
				hints.Workloads = append(hints.Workloads, workload)
			}
			for _, alias := range expandWorkloadAliases(workload) {
				if _, ok := aliasSeen[alias]; ok || alias == "" {
					continue
				}
				aliasSeen[alias] = struct{}{}
				hints.Aliases = append(hints.Aliases, alias)
			}
		}
	}

	sort.Strings(hints.Namespaces)
	sort.Strings(hints.Workloads)
	sort.Strings(hints.Aliases)
	sort.Strings(hints.Envs)
	return hints
}

func inferEnvHints(namespace string) []string {
	known := map[string]struct{}{
		"prod": {}, "production": {}, "test": {}, "testing": {}, "stage": {}, "staging": {}, "dev": {}, "development": {}, "qa": {}, "uat": {},
	}
	var out []string
	for _, part := range strings.Split(strings.ToLower(namespace), "-") {
		if _, ok := known[part]; ok {
			out = append(out, normalizeEnv(part))
		}
	}
	return uniqueStrings(out)
}

func normalizeEnv(env string) string {
	switch env {
	case "production":
		return "prod"
	case "testing":
		return "test"
	case "staging":
		return "stage"
	case "development":
		return "dev"
	default:
		return env
	}
}

func expandWorkloadAliases(workload string) []string {
	workload = strings.ToLower(strings.TrimSpace(workload))
	if workload == "" {
		return nil
	}
	out := []string{workload}
	tokens := tokenizeIdentifier(workload)
	generic := map[string]struct{}{
		"deploy": {}, "deployment": {}, "service": {}, "server": {}, "operator": {}, "worker": {}, "frontend": {}, "backend": {}, "matching": {}, "history": {}, "web": {}, "app": {}, "kube": {},
	}
	for _, token := range tokens {
		if len(token) < 4 {
			continue
		}
		if _, skip := generic[token]; skip {
			continue
		}
		out = append(out, token)
		if strings.HasSuffix(token, "io") && len(token) > 4 {
			out = append(out, strings.TrimSuffix(token, "io"))
		}
	}
	if len(tokens) >= 2 {
		out = append(out, strings.Join(tokens[len(tokens)-2:], "-"))
	}
	return uniqueStrings(out)
}

func semanticCandidateBonuses(files []repoFile, semantic semanticHints) map[string]int {
	bonuses := map[string]int{}
	if len(semantic.Aliases) == 0 && len(semantic.Envs) == 0 {
		return bonuses
	}
	for _, file := range files {
		lowerRel := strings.ToLower(file.rel)
		for _, alias := range semantic.Aliases {
			if alias != "" && strings.Contains(lowerRel, alias) {
				bonuses[file.rel] += 18
			}
		}
		for _, env := range semantic.Envs {
			if env == "" {
				continue
			}
			if strings.Contains(lowerRel, "/"+env+"/") || strings.Contains(lowerRel, "-"+env+".yaml") || strings.Contains(lowerRel, "-"+env+".yml") {
				bonuses[file.rel] += 14
			}
		}
	}
	return bonuses
}

func tokenizeIdentifier(s string) []string {
	replacer := strings.NewReplacer("/", " ", "-", " ", "_", " ", ".", " ", ":", " ", "\n", " ", "\t", " ")
	s = replacer.Replace(strings.ToLower(s))
	var out []string
	for _, token := range strings.Fields(s) {
		if len(token) < 2 {
			continue
		}
		out = append(out, token)
	}
	return uniqueStrings(out)
}

func uniqueStrings(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func joinListOrNone(items []string) string {
	if len(items) == 0 {
		return "none"
	}
	return strings.Join(items, ", ")
}

func limitStrings(in []string, n int) []string {
	if n <= 0 || len(in) <= n {
		return in
	}
	return in[:n]
}

func minInt(a, b int) int {
	if a <= 0 {
		return b
	}
	if a < b {
		return a
	}
	return b
}
