package infra

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Executor struct {
	RepoPath      string
	SkipFmt       bool
	RunValidate   bool
	Push          bool
	OpenPR        bool
	BaseBranch    string
	RequireClean  bool
	Progress      func(string, ...any)
	RepoAgent     PlanRefiner
	RepoInventory string
}

func (e *Executor) Apply(ctx context.Context, plan PRPlan) (Result, error) {
	if err := plan.Validate(); err != nil {
		return Result{}, err
	}
	repoPath := strings.TrimSpace(e.RepoPath)
	if repoPath == "" {
		return Result{}, fmt.Errorf("repo path is required")
	}
	if st, err := os.Stat(repoPath); err != nil || !st.IsDir() {
		return Result{}, fmt.Errorf("repo path is not a directory: %s", repoPath)
	}
	started := time.Now()
	effectivePlan := plan

	if e.RequireClean {
		e.progress("checking git worktree cleanliness")
		clean, out, err := gitClean(ctx, repoPath)
		if err != nil {
			return Result{}, err
		}
		if !clean {
			return Result{}, fmt.Errorf("refusing to edit dirty repo; commit or stash changes first\n%s", out)
		}
	}

	if e.RepoAgent != nil {
		e.progress("running repo agent inside infra repo")
		refined, err := e.RepoAgent.Refine(ctx, RefineRequest{
			RepoPath:      repoPath,
			RepoInventory: e.RepoInventory,
			Plan:          effectivePlan,
		})
		if err != nil {
			return Result{}, fmt.Errorf("repo agent refine failed: %w", err)
		}
		effectivePlan = refined.Plan
		if narrative := strings.TrimSpace(refined.Narrative); narrative != "" {
			e.progress("repo agent refined plan: %s", narrative)
		}
		if err := effectivePlan.Validate(); err != nil {
			return Result{}, fmt.Errorf("effective infra plan is invalid: %w", err)
		}
		if refined.DirectEdits {
			changed, err := gitChangedFiles(ctx, repoPath)
			if err != nil {
				return Result{}, err
			}
			if len(changed) == 0 {
				return Result{}, fmt.Errorf("repo agent reported direct edits but git detected no changed files")
			}
			e.progress("repo agent applied direct edits to %d file(s)", len(changed))
			applied := changed
			return e.finishApply(ctx, repoPath, effectivePlan, applied, started)
		}
	}
	if err := effectivePlan.Validate(); err != nil {
		return Result{}, fmt.Errorf("effective infra plan is invalid: %w", err)
	}

	e.progress("applying %d repo edit(s)", len(effectivePlan.Edits))
	applied, err := applyEdits(repoPath, effectivePlan.Edits)
	if err != nil {
		return Result{}, err
	}
	return e.finishApply(ctx, repoPath, effectivePlan, applied, started)
}

func (e *Executor) finishApply(ctx context.Context, repoPath string, effectivePlan PRPlan, applied []string, started time.Time) (Result, error) {
	validationOutput := []string{}
	if !e.SkipFmt {
		e.progress("running %s formatter", effectivePlan.Backend)
		if out, err := runBackendFmt(ctx, repoPath, effectivePlan.Backend); err != nil {
			return Result{}, fmt.Errorf("%s fmt failed: %w\n%s", effectivePlan.Backend, err, out)
		} else if strings.TrimSpace(out) != "" {
			validationOutput = append(validationOutput, out)
		}
	}
	if e.RunValidate {
		e.progress("running %s validation", effectivePlan.Backend)
		outputs, err := runBackendValidate(ctx, repoPath, effectivePlan.Backend, applied)
		if err != nil {
			return Result{}, err
		}
		for _, out := range outputs {
			if strings.TrimSpace(out) != "" {
				validationOutput = append(validationOutput, out)
			}
		}
	}

	e.progress("creating branch %s", effectivePlan.BranchName)
	if _, err := runCmd(ctx, repoPath, "git", "checkout", "-b", effectivePlan.BranchName); err != nil {
		return Result{}, fmt.Errorf("git checkout -b failed: %w", err)
	}
	e.progress("staging changed files")
	addArgs := []string{"add"}
	addArgs = append(addArgs, applied...)
	if _, err := runCmd(ctx, repoPath, "git", addArgs...); err != nil {
		return Result{}, fmt.Errorf("git add failed: %w", err)
	}
	e.progress("creating git commit")
	if _, err := runCmd(ctx, repoPath, "git", "commit", "-m", effectivePlan.CommitMessage); err != nil {
		return Result{}, fmt.Errorf("git commit failed: %w", err)
	}
	e.progress("reading commit SHA")
	sha, err := runCmd(ctx, repoPath, "git", "rev-parse", "HEAD")
	if err != nil {
		return Result{}, fmt.Errorf("git rev-parse failed: %w", err)
	}

	result := Result{
		Backend:          effectivePlan.Backend,
		BranchName:       effectivePlan.BranchName,
		CommitSHA:        strings.TrimSpace(sha),
		AppliedFiles:     applied,
		StartedAt:        started,
		EndedAt:          time.Now(),
		ValidationOutput: validationOutput,
	}

	if e.Push {
		e.progress("pushing branch to origin")
		args := []string{"push", "-u", "origin", effectivePlan.BranchName}
		if _, err := runCmd(ctx, repoPath, "git", args...); err != nil {
			return Result{}, fmt.Errorf("git push failed: %w", err)
		}
		result.Pushed = true
	}
	if e.OpenPR {
		e.progress("opening GitHub pull request")
		args := []string{"pr", "create", "--title", effectivePlan.PRTitle, "--body", buildPRBody(effectivePlan, result), "--head", effectivePlan.BranchName}
		if strings.TrimSpace(e.BaseBranch) != "" {
			args = append(args, "--base", e.BaseBranch)
		}
		out, err := runCmd(ctx, repoPath, "gh", args...)
		if err != nil {
			return Result{}, fmt.Errorf("gh pr create failed: %w\n%s", err, out)
		}
		result.PullRequestURL = firstURL(out)
	}
	result.EndedAt = time.Now()
	return result, nil
}

func (e *Executor) progress(format string, args ...any) {
	if e.Progress != nil {
		e.Progress(format, args...)
	}
}

func gitChangedFiles(ctx context.Context, repoPath string) ([]string, error) {
	changedSet := map[string]struct{}{}
	for _, args := range [][]string{
		{"diff", "--name-only", "--relative"},
		{"ls-files", "--others", "--exclude-standard"},
	} {
		out, err := runCmd(ctx, repoPath, "git", args...)
		if err != nil {
			return nil, fmt.Errorf("git %s failed: %w", strings.Join(args, " "), err)
		}
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			changedSet[line] = struct{}{}
		}
	}
	files := make([]string, 0, len(changedSet))
	for file := range changedSet {
		files = append(files, file)
	}
	sort.Strings(files)
	return files, nil
}

func buildPRBody(plan PRPlan, result Result) string {
	var b strings.Builder
	if strings.TrimSpace(plan.PRBody) != "" {
		b.WriteString(strings.TrimSpace(plan.PRBody))
		b.WriteString("\n\n")
	}
	if strings.TrimSpace(plan.AgentPrompt) != "" {
		b.WriteString("## Agent Brief\n")
		b.WriteString(strings.TrimSpace(plan.AgentPrompt))
		b.WriteString("\n\n")
	}
	b.WriteString("## Change Summary\n")
	b.WriteString(strings.TrimSpace(plan.Summary))
	b.WriteString("\n\n## Changed Files\n")
	if len(result.AppliedFiles) == 0 {
		b.WriteString("- none recorded\n")
	} else {
		for _, file := range result.AppliedFiles {
			b.WriteString("- " + file + "\n")
		}
	}
	b.WriteString("\n## Verification Checklist\n")
	if len(plan.Verify.Commands) == 0 {
		b.WriteString("- Review the changed files and run the repo's normal validation flow\n")
	} else {
		for _, cmd := range plan.Verify.Commands {
			b.WriteString("- `" + strings.TrimSpace(cmd) + "`\n")
		}
	}
	if len(plan.Verify.Notes) > 0 {
		b.WriteString("\n## Verification Notes\n")
		for _, note := range plan.Verify.Notes {
			b.WriteString("- " + strings.TrimSpace(note) + "\n")
		}
	}
	b.WriteString("\n## Risk Matrix\n")
	for _, line := range buildRiskMatrix(plan, result) {
		b.WriteString("- " + line + "\n")
	}
	b.WriteString("\n## Post-Merge Checklist\n")
	for _, line := range buildPostMergeChecklist(plan, result) {
		b.WriteString("- " + line + "\n")
	}
	b.WriteString("\n## Rollback\n")
	b.WriteString("- Revert this PR or revert the merge commit if the change causes instability.\n")
	return strings.TrimSpace(b.String())
}

func buildRiskMatrix(plan PRPlan, result Result) []string {
	lines := []string{
		"Change type: infrastructure-as-code update via " + string(plan.Backend),
		"Blast radius: " + inferBlastRadius(result.AppliedFiles),
		"Validation confidence: " + inferValidationConfidence(plan, result),
		"Operational risk: " + inferOperationalRisk(plan, result),
	}
	if v := strings.TrimSpace(plan.Metadata["risk_note"]); v != "" {
		lines = append(lines, "Plan-specific note: "+v)
	}
	return lines
}

func buildPostMergeChecklist(plan PRPlan, result Result) []string {
	lines := []string{
		"Watch the next infrastructure rollout/apply that consumes this repo change.",
		"Verify the targeted workload or module converges without drift.",
		"Confirm no unexpected changes appeared in adjacent modules or stacks.",
	}
	if len(plan.Verify.Commands) > 0 {
		lines = append(lines, "Re-run the repository validation flow after merge if your CI does not already cover it.")
	}
	if strings.TrimSpace(plan.Metadata["post_merge_check"]) != "" {
		lines = append(lines, strings.TrimSpace(plan.Metadata["post_merge_check"]))
	}
	return lines
}

func inferBlastRadius(files []string) string {
	if len(files) <= 1 {
		return "low (single file/module candidate)"
	}
	if len(files) <= 3 {
		return "medium (small set of files)"
	}
	return "elevated (multiple files/modules touched)"
}

func inferValidationConfidence(plan PRPlan, result Result) string {
	if len(result.ValidationOutput) > 0 {
		return "medium-high (executor validation produced output)"
	}
	if len(plan.Verify.Commands) > 0 {
		return "medium (verification commands declared but not all may have run)"
	}
	return "low-medium (manual verification still important)"
}

func inferOperationalRisk(plan PRPlan, result Result) string {
	for _, edit := range plan.Edits {
		if edit.Type == EditTypeHCLReplaceBlock {
			return "medium-high (block replacement can affect more than one attribute)"
		}
	}
	if len(result.AppliedFiles) > 2 {
		return "medium"
	}
	return "low-medium"
}

func applyEdits(repoPath string, edits []Edit) ([]string, error) {
	appliedSet := map[string]struct{}{}
	for _, edit := range edits {
		target := filepath.Join(repoPath, filepath.Clean(edit.Path))
		out, err := applyEdit(target, edit)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(target, []byte(out), 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", edit.Path, err)
		}
		appliedSet[edit.Path] = struct{}{}
	}
	files := make([]string, 0, len(appliedSet))
	for path := range appliedSet {
		files = append(files, path)
	}
	sort.Strings(files)
	return files, nil
}

func applyEdit(target string, edit Edit) (string, error) {
	content, err := os.ReadFile(target)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", target, err)
	}
	src := string(content)
	editType := edit.Type
	if editType == "" {
		if strings.TrimSpace(edit.BlockType) != "" && strings.TrimSpace(edit.Attribute) != "" && strings.TrimSpace(edit.ValueHCL) != "" {
			editType = EditTypeHCLSetAttribute
		} else {
			editType = EditTypeSearchReplace
		}
	}
	switch editType {
	case EditTypeSearchReplace:
		count := strings.Count(src, edit.Search)
		if count == 0 {
			return "", fmt.Errorf("edit target not found in %s", target)
		}
		if count > 1 {
			return "", fmt.Errorf("edit target matched multiple times in %s; refine snippet", target)
		}
		return strings.Replace(src, edit.Search, edit.Replace, 1), nil
	case EditTypeHCLSetAttribute:
		return setHCLAttribute(src, edit)
	case EditTypeHCLDeleteAttribute:
		return deleteHCLAttribute(src, edit)
	case EditTypeHCLReplaceBlock:
		return replaceHCLBlock(src, edit)
	case EditTypeHCLAppendBlockBody:
		return appendHCLBlockBody(src, edit)
	default:
		return "", fmt.Errorf("unsupported edit type: %q", editType)
	}
}

func setHCLAttribute(src string, edit Edit) (string, error) {
	start, end, indent, err := locateBlock(src, edit.BlockType, edit.Labels)
	if err != nil {
		return "", err
	}
	block := src[start:end]
	if attrStart, attrEnd, attrIndent, ok := findTopLevelAttribute(block, edit.Attribute); ok {
		replacement := formatHCLAttribute(attrIndent, edit.Attribute, edit.ValueHCL)
		block = block[:attrStart] + replacement + block[attrEnd:]
	} else {
		insert := "\n" + formatHCLAttribute(indent+"  ", edit.Attribute, edit.ValueHCL) + "\n"
		block = replaceBlockClosingBrace(block, insert, indent)
	}
	return src[:start] + block + src[end:], nil
}

func deleteHCLAttribute(src string, edit Edit) (string, error) {
	start, end, _, err := locateBlock(src, edit.BlockType, edit.Labels)
	if err != nil {
		return "", err
	}
	block := src[start:end]
	attrStart, attrEnd, _, ok := findTopLevelAttribute(block, edit.Attribute)
	if !ok {
		return "", fmt.Errorf("attribute %q not found in target block", edit.Attribute)
	}
	block = block[:attrStart] + block[attrEnd:]
	return src[:start] + block + src[end:], nil
}

func replaceHCLBlock(src string, edit Edit) (string, error) {
	start, end, indent, err := locateBlock(src, edit.BlockType, edit.Labels)
	if err != nil {
		return "", err
	}
	replacement := reindentBlock(edit.BlockHCL, indent)
	return src[:start] + replacement + src[end:], nil
}

func appendHCLBlockBody(src string, edit Edit) (string, error) {
	start, end, indent, err := locateBlock(src, edit.BlockType, edit.Labels)
	if err != nil {
		return "", err
	}
	block := src[start:end]
	body := strings.TrimSpace(edit.BlockHCL)
	if body == "" {
		return "", fmt.Errorf("blockHCL is empty")
	}
	insertion := "\n" + reindentMultiline(body, indent+"  ") + "\n"
	block = replaceBlockClosingBrace(block, insertion, indent)
	return src[:start] + block + src[end:], nil
}

func findTopLevelAttribute(block, attribute string) (start int, end int, indent string, ok bool) {
	attrRe := regexp.MustCompile(`(?ms)^([ \t]*)` + regexp.QuoteMeta(attribute) + `[ \t]*=.*?(?=^\1(?:[A-Za-z0-9_-]+(?:[ \t]+"[^"]+")*[ \t]*(?:=|\{)|\})|\z)`)
	loc := attrRe.FindStringSubmatchIndex(block)
	if loc == nil {
		return 0, 0, "", false
	}
	return loc[0], loc[1], block[loc[2]:loc[3]], true
}

func formatHCLAttribute(indent, attribute, value string) string {
	lines := normalizeSnippetLines(value)
	if len(lines) == 0 {
		return fmt.Sprintf("%s%s = null", indent, attribute)
	}
	if len(lines) == 1 {
		return fmt.Sprintf("%s%s = %s", indent, attribute, lines[0])
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s%s = %s", indent, attribute, lines[0]))
	for _, line := range lines[1:] {
		b.WriteString("\n")
		if strings.TrimSpace(line) == "" {
			continue
		}
		b.WriteString(indent)
		b.WriteString(line)
	}
	return b.String()
}

func replaceBlockClosingBrace(block, insertion, indent string) string {
	closeIdx := strings.LastIndex(block, indent+"}")
	if closeIdx == -1 {
		closeIdx = strings.LastIndex(block, "}")
	}
	if closeIdx == -1 {
		return block + insertion
	}
	prefix := strings.TrimRight(block[:closeIdx], "\n")
	return prefix + insertion + indent + "}"
}

func locateBlock(src, blockType string, labels []string) (start int, end int, indent string, err error) {
	header := buildBlockHeader(blockType, labels)
	idx := strings.Index(src, header)
	if idx == -1 {
		return 0, 0, "", fmt.Errorf("block not found: %s", header)
	}
	lineStart := strings.LastIndex(src[:idx], "\n") + 1
	indent = src[lineStart:idx]
	openIdx := strings.Index(src[idx:], "{")
	if openIdx == -1 {
		return 0, 0, "", fmt.Errorf("block opening brace not found: %s", header)
	}
	openIdx += idx
	depth := 0
	inString := false
	escaped := false
	for i := openIdx; i < len(src); i++ {
		ch := src[i]
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
				return idx, i + 1, indent, nil
			}
		}
	}
	return 0, 0, "", fmt.Errorf("block closing brace not found: %s", header)
}

func buildBlockHeader(blockType string, labels []string) string {
	var b strings.Builder
	b.WriteString(blockType)
	for _, label := range labels {
		if strings.TrimSpace(label) == "" {
			continue
		}
		b.WriteString(` "`)
		b.WriteString(label)
		b.WriteString(`"`)
	}
	b.WriteString(" {")
	return b.String()
}

func reindentBlock(block, indent string) string {
	lines := normalizeSnippetLines(block)
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			lines[i] = ""
			continue
		}
		lines[i] = indent + strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
}

func reindentMultiline(body, indent string) string {
	lines := normalizeSnippetLines(body)
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			lines[i] = ""
			continue
		}
		lines[i] = indent + strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
}

func normalizeSnippetLines(body string) []string {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return nil
	}
	lines := strings.Split(trimmed, "\n")
	minIndent := -1
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if i == 0 {
			lines[i] = strings.TrimSpace(line)
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if minIndent == -1 || indent < minIndent {
			minIndent = indent
		}
	}
	if minIndent > 0 {
		for i := 1; i < len(lines); i++ {
			line := lines[i]
			if strings.TrimSpace(line) == "" {
				continue
			}
			if len(line) >= minIndent {
				lines[i] = line[minIndent:]
			}
		}
	}
	return lines
}

func runBackendFmt(ctx context.Context, repoPath string, backend BackendType) (string, error) {
	cmd, args := backendFmtCommand(backend)
	return runCmd(ctx, repoPath, cmd, args...)
}

func backendFmtCommand(backend BackendType) (string, []string) {
	switch backend {
	case BackendOpenTofu:
		return "tofu", []string{"fmt", "-recursive"}
	case BackendTerragrunt:
		return "terragrunt", []string{"hclfmt"}
	default:
		return "terraform", []string{"fmt", "-recursive"}
	}
}

func backendValidateCommand(backend BackendType) (string, []string) {
	switch backend {
	case BackendOpenTofu:
		return "tofu", []string{"validate"}
	case BackendTerragrunt:
		return "terragrunt", []string{"validate"}
	default:
		return "terraform", []string{"validate"}
	}
}

func runBackendValidate(ctx context.Context, repoPath string, backend BackendType, appliedFiles []string) ([]string, error) {
	dirs := changedDirs(appliedFiles)
	switch backend {
	case BackendTerragrunt:
		if len(dirs) == 0 {
			dirs = []string{"."}
		}
		var outputs []string
		for _, dir := range dirs {
			full := filepath.Join(repoPath, dir)
			if _, err := os.Stat(filepath.Join(full, "terragrunt.hcl")); err == nil {
				if out, err := runCmd(ctx, full, "terragrunt", "hclvalidate"); err != nil {
					return nil, fmt.Errorf("terragrunt hclvalidate failed in %s: %w\n%s", dir, err, out)
				} else {
					outputs = append(outputs, fmt.Sprintf("[%s] terragrunt hclvalidate\n%s", dir, out))
				}
				if out, err := runCmd(ctx, full, "terragrunt", "validate-inputs"); err != nil {
					return nil, fmt.Errorf("terragrunt validate-inputs failed in %s: %w\n%s", dir, err, out)
				} else {
					outputs = append(outputs, fmt.Sprintf("[%s] terragrunt validate-inputs\n%s", dir, out))
				}
			}
		}
		if len(outputs) == 0 {
			out, err := runCmd(ctx, repoPath, "terragrunt", "hclvalidate")
			if err != nil {
				return nil, fmt.Errorf("terragrunt hclvalidate failed: %w\n%s", err, out)
			}
			outputs = append(outputs, out)
		}
		return outputs, nil
	case BackendOpenTofu, BackendTerraform:
		cmd, args := backendValidateCommand(backend)
		var outputs []string
		if len(dirs) == 0 {
			out, err := runCmd(ctx, repoPath, cmd, args...)
			if err != nil {
				return nil, fmt.Errorf("%s validate failed: %w\n%s", backend, err, out)
			}
			return []string{out}, nil
		}
		for _, dir := range dirs {
			full := filepath.Join(repoPath, dir)
			if !hasTerraformFiles(full) {
				continue
			}
			out, err := runCmd(ctx, full, cmd, args...)
			if err != nil {
				return nil, fmt.Errorf("%s validate failed in %s: %w\n%s", backend, dir, err, out)
			}
			outputs = append(outputs, fmt.Sprintf("[%s] %s %s\n%s", dir, cmd, strings.Join(args, " "), out))
		}
		if len(outputs) == 0 {
			out, err := runCmd(ctx, repoPath, cmd, args...)
			if err != nil {
				return nil, fmt.Errorf("%s validate failed: %w\n%s", backend, err, out)
			}
			outputs = append(outputs, out)
		}
		return outputs, nil
	default:
		return nil, fmt.Errorf("unsupported backend validate strategy: %s", backend)
	}
}

func changedDirs(appliedFiles []string) []string {
	set := map[string]struct{}{}
	for _, file := range appliedFiles {
		dir := filepath.Dir(file)
		if dir == "" {
			dir = "."
		}
		set[dir] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for dir := range set {
		out = append(out, dir)
	}
	sort.Strings(out)
	return out
}

func hasTerraformFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext == ".tf" || ext == ".tfvars" || ext == ".hcl" {
			return true
		}
	}
	return false
}

func gitClean(ctx context.Context, repoPath string) (bool, string, error) {
	out, err := runCmd(ctx, repoPath, "git", "status", "--porcelain")
	if err != nil {
		return false, out, fmt.Errorf("git status failed: %w", err)
	}
	return strings.TrimSpace(out) == "", out, nil
}

func runCmd(ctx context.Context, dir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func firstURL(s string) string {
	for _, field := range strings.Fields(s) {
		if strings.HasPrefix(field, "http://") || strings.HasPrefix(field, "https://") {
			return field
		}
	}
	return ""
}
