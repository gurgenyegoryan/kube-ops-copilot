package hclresolver

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

type Resolver struct {
	RepoPath string
	parser   *hclparse.Parser
	files    map[string]*parsedFile
}

type parsedFile struct {
	rel  string
	abs  string
	body *hclsyntax.Body
}

func New(repoPath string) *Resolver {
	return &Resolver{
		RepoPath: repoPath,
		parser:   hclparse.NewParser(),
		files:    map[string]*parsedFile{},
	}
}

func (r *Resolver) DiscoverRefs(rel string) ([]string, error) {
	rel = filepath.Clean(rel)
	file, err := r.load(rel)
	if err != nil {
		return nil, err
	}
	ctx, err := r.evalContext(rel, map[string]bool{})
	if err != nil {
		return nil, err
	}
	refs := map[string]struct{}{}
	r.collectBodyRefs(file.body, rel, ctx, refs)
	out := make([]string, 0, len(refs))
	for ref := range refs {
		out = append(out, ref)
	}
	sort.Strings(out)
	return out, nil
}

func (r *Resolver) collectBodyRefs(body *hclsyntax.Body, rel string, ctx *hcl.EvalContext, refs map[string]struct{}) {
	for name, attr := range body.Attributes {
		if !looksLikeReferenceAttribute(name) {
			continue
		}
		for _, ref := range r.resolveExpressionStrings(rel, attr.Expr, ctx) {
			refs[ref] = struct{}{}
		}
	}
	for _, block := range body.Blocks {
		switch block.Type {
		case "terraform", "module", "dependency", "include", "locals":
			r.collectBodyRefs(block.Body, rel, ctx, refs)
		default:
			r.collectBodyRefs(block.Body, rel, ctx, refs)
		}
	}
}

func (r *Resolver) resolveExpressionStrings(rel string, expr hclsyntax.Expression, ctx *hcl.EvalContext) []string {
	value, diags := expr.Value(ctx)
	if diags.HasErrors() {
		return nil
	}
	return r.resolveCTYStrings(rel, value)
}

func (r *Resolver) resolveCTYStrings(rel string, value cty.Value) []string {
	if !value.IsKnown() || value.IsNull() {
		return nil
	}
	var raw []string
	switch {
	case value.Type() == cty.String:
		raw = append(raw, value.AsString())
	case value.Type().IsTupleType() || value.Type().IsListType() || value.Type().IsSetType():
		it := value.ElementIterator()
		for it.Next() {
			_, elem := it.Element()
			raw = append(raw, r.resolveCTYStrings(rel, elem)...)
		}
	default:
		return nil
	}

	baseDir := filepath.Dir(rel)
	if baseDir == "" {
		baseDir = "."
	}
	seen := map[string]struct{}{}
	var out []string
	for _, item := range raw {
		for _, resolved := range resolveRepoLocalPath(r.RepoPath, baseDir, item) {
			if _, ok := seen[resolved]; ok {
				continue
			}
			seen[resolved] = struct{}{}
			out = append(out, resolved)
		}
	}
	sort.Strings(out)
	return out
}

func (r *Resolver) evalContext(rel string, stack map[string]bool) (*hcl.EvalContext, error) {
	if stack[rel] {
		return &hcl.EvalContext{Functions: r.functions(rel, stack), Variables: map[string]cty.Value{
			"local":      cty.EmptyObjectVal,
			"include":    cty.EmptyObjectVal,
			"dependency": cty.EmptyObjectVal,
			"path": objectValue(map[string]cty.Value{
				"module": cty.StringVal(filepath.Join(r.RepoPath, filepath.Dir(rel))),
			}),
		}}, nil
	}
	stack[rel] = true
	defer delete(stack, rel)

	includes, _ := r.evaluateIncludes(rel, stack)
	dependencies, _ := r.evaluateDependencies(rel, stack)
	locals, _ := r.evaluateLocals(rel, includes, dependencies, stack)

	return &hcl.EvalContext{
		Functions: r.functions(rel, stack),
		Variables: map[string]cty.Value{
			"local":      objectValue(locals),
			"include":    objectValue(includes),
			"dependency": objectValue(dependencies),
			"path": objectValue(map[string]cty.Value{
				"module": cty.StringVal(filepath.Join(r.RepoPath, filepath.Dir(rel))),
			}),
		},
	}, nil
}

func (r *Resolver) evaluateLocals(rel string, includes, dependencies map[string]cty.Value, stack map[string]bool) (map[string]cty.Value, error) {
	file, err := r.load(rel)
	if err != nil {
		return nil, err
	}
	values := map[string]cty.Value{}
	localAttrs := map[string]*hclsyntax.Attribute{}
	for _, block := range file.body.Blocks {
		if block.Type != "locals" {
			continue
		}
		for name, attr := range block.Body.Attributes {
			localAttrs[name] = attr
		}
	}
	if len(localAttrs) == 0 {
		return values, nil
	}
	for pass := 0; pass < len(localAttrs)+2; pass++ {
		progress := false
		ctx := &hcl.EvalContext{
			Functions: r.functions(rel, stack),
			Variables: map[string]cty.Value{
				"local":      objectValue(values),
				"include":    objectValue(includes),
				"dependency": objectValue(dependencies),
				"path": objectValue(map[string]cty.Value{
					"module": cty.StringVal(filepath.Join(r.RepoPath, filepath.Dir(rel))),
				}),
			},
		}
		for name, attr := range localAttrs {
			if _, ok := values[name]; ok {
				continue
			}
			value, diags := attr.Expr.Value(ctx)
			if diags.HasErrors() || !value.IsKnown() {
				continue
			}
			values[name] = value
			progress = true
		}
		if !progress {
			break
		}
	}
	return values, nil
}

func (r *Resolver) evaluateIncludes(rel string, stack map[string]bool) (map[string]cty.Value, error) {
	file, err := r.load(rel)
	if err != nil {
		return nil, err
	}
	values := map[string]cty.Value{}
	for _, block := range file.body.Blocks {
		if block.Type != "include" || len(block.Labels) == 0 {
			continue
		}
		includeCtx := &hcl.EvalContext{Functions: r.functions(rel, stack), Variables: map[string]cty.Value{
			"path": objectValue(map[string]cty.Value{
				"module": cty.StringVal(filepath.Join(r.RepoPath, filepath.Dir(rel))),
			}),
		}}
		pathAttr, ok := block.Body.Attributes["path"]
		if !ok {
			continue
		}
		pathVals := r.resolveExpressionStrings(rel, pathAttr.Expr, includeCtx)
		if len(pathVals) == 0 {
			continue
		}
		target := pathVals[0]
		targetFile := target
		if info, err := os.Stat(filepath.Join(r.RepoPath, target)); err == nil && info.IsDir() {
			targetFile = filepath.Join(target, "terragrunt.hcl")
		}
		targetFile = filepath.Clean(targetFile)
		locals, err := r.evaluateLocals(targetFile, nil, nil, stack)
		if err != nil {
			continue
		}
		exposed := false
		if attr, ok := block.Body.Attributes["expose"]; ok {
			if value, diags := attr.Expr.Value(includeCtx); !diags.HasErrors() && value.Type() == cty.Bool && value.IsKnown() {
				exposed = value.True()
			}
		}
		if exposed {
			values[block.Labels[0]] = objectValue(map[string]cty.Value{
				"locals": objectValue(locals),
			})
		}
	}
	return values, nil
}

func (r *Resolver) evaluateInputs(rel string, includes, dependencies, locals map[string]cty.Value, stack map[string]bool) (cty.Value, error) {
	file, err := r.load(rel)
	if err != nil {
		return cty.EmptyObjectVal, err
	}
	attr, ok := file.body.Attributes["inputs"]
	if !ok {
		return cty.EmptyObjectVal, nil
	}
	ctx := &hcl.EvalContext{
		Functions: r.functions(rel, stack),
		Variables: map[string]cty.Value{
			"local":      objectValue(locals),
			"include":    objectValue(includes),
			"dependency": objectValue(dependencies),
			"path": objectValue(map[string]cty.Value{
				"module": cty.StringVal(filepath.Join(r.RepoPath, filepath.Dir(rel))),
			}),
		},
	}
	value, diags := attr.Expr.Value(ctx)
	if diags.HasErrors() || !value.IsKnown() || value.IsNull() {
		return cty.EmptyObjectVal, nil
	}
	return value, nil
}

func (r *Resolver) evaluateDependencies(rel string, stack map[string]bool) (map[string]cty.Value, error) {
	file, err := r.load(rel)
	if err != nil {
		return nil, err
	}
	values := map[string]cty.Value{}
	ctx := &hcl.EvalContext{Functions: r.functions(rel, stack), Variables: map[string]cty.Value{
		"path": objectValue(map[string]cty.Value{
			"module": cty.StringVal(filepath.Join(r.RepoPath, filepath.Dir(rel))),
		}),
	}}
	for _, block := range file.body.Blocks {
		if block.Type != "dependency" || len(block.Labels) == 0 {
			continue
		}
		attr, ok := block.Body.Attributes["config_path"]
		if !ok {
			continue
		}
		resolved := r.resolveExpressionStrings(rel, attr.Expr, ctx)
		if len(resolved) == 0 {
			continue
		}
		values[block.Labels[0]] = objectValue(map[string]cty.Value{
			"config_path": cty.StringVal(resolved[0]),
		})
	}
	return values, nil
}

func (r *Resolver) evaluateTerragruntConfig(rel string, stack map[string]bool) (cty.Value, error) {
	if stack[rel] {
		return objectValue(map[string]cty.Value{
			"locals": cty.EmptyObjectVal,
			"inputs": cty.EmptyObjectVal,
		}), nil
	}
	stack[rel] = true
	defer delete(stack, rel)

	includes, _ := r.evaluateIncludes(rel, stack)
	dependencies, _ := r.evaluateDependencies(rel, stack)
	locals, _ := r.evaluateLocals(rel, includes, dependencies, stack)
	inputs, _ := r.evaluateInputs(rel, includes, dependencies, locals, stack)
	if inputs == cty.NilVal {
		inputs = cty.EmptyObjectVal
	}
	return objectValue(map[string]cty.Value{
		"locals": objectValue(locals),
		"inputs": normalizeObjectLike(inputs),
	}), nil
}

func (r *Resolver) functions(rel string, stack map[string]bool) map[string]function.Function {
	absDir := filepath.Join(r.RepoPath, filepath.Dir(rel))
	return map[string]function.Function{
		"get_original_terragrunt_dir": simpleNoArgStringFunc(func() (string, error) {
			return absDir, nil
		}),
		"get_terragrunt_dir": simpleNoArgStringFunc(func() (string, error) {
			return absDir, nil
		}),
		"find_in_parent_folders": function.New(&function.Spec{
			VarParam: &function.Parameter{Type: cty.String},
			Type:     function.StaticReturnType(cty.String),
			Impl: func(args []cty.Value, ret cty.Type) (cty.Value, error) {
				name := "terragrunt.hcl"
				if len(args) > 0 && args[0].Type() == cty.String && args[0].IsKnown() && !args[0].IsNull() {
					name = args[0].AsString()
				}
				found, err := findInParentFolders(absDir, name)
				if err != nil {
					return cty.DynamicVal, err
				}
				return cty.StringVal(found), nil
			},
		}),
		"read_terragrunt_config": function.New(&function.Spec{
			Params: []function.Parameter{{Type: cty.String}},
			Type:   function.StaticReturnType(cty.DynamicPseudoType),
			Impl: func(args []cty.Value, ret cty.Type) (cty.Value, error) {
				target := strings.TrimSpace(args[0].AsString())
				if target == "" {
					return cty.EmptyObjectVal, nil
				}
				targetFile := target
				if info, err := os.Stat(target); err == nil && info.IsDir() {
					targetFile = filepath.Join(target, "terragrunt.hcl")
				}
				if !filepath.IsAbs(targetFile) {
					targetFile = filepath.Join(absDir, targetFile)
				}
				relPath, err := filepath.Rel(r.RepoPath, targetFile)
				if err != nil || relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
					return cty.EmptyObjectVal, nil
				}
				cfg, err := r.evaluateTerragruntConfig(filepath.Clean(relPath), stack)
				if err != nil {
					return cty.EmptyObjectVal, nil
				}
				return cfg, nil
			},
		}),
		"merge": function.New(&function.Spec{
			VarParam: &function.Parameter{Type: cty.DynamicPseudoType},
			Type:     function.StaticReturnType(cty.DynamicPseudoType),
			Impl: func(args []cty.Value, ret cty.Type) (cty.Value, error) {
				merged := map[string]cty.Value{}
				for _, arg := range args {
					if !arg.IsKnown() || arg.IsNull() {
						continue
					}
					for key, value := range flattenObjectLike(arg) {
						merged[key] = value
					}
				}
				return objectValue(merged), nil
			},
		}),
		"dirname":  simpleUnaryStringFunc(filepath.Dir),
		"basename": simpleUnaryStringFunc(filepath.Base),
		"joinpath": function.New(&function.Spec{
			VarParam: &function.Parameter{Type: cty.String},
			Type:     function.StaticReturnType(cty.String),
			Impl: func(args []cty.Value, ret cty.Type) (cty.Value, error) {
				parts := make([]string, 0, len(args))
				for _, arg := range args {
					parts = append(parts, arg.AsString())
				}
				return cty.StringVal(filepath.Clean(filepath.Join(parts...))), nil
			},
		}),
	}
}

func (r *Resolver) load(rel string) (*parsedFile, error) {
	rel = filepath.Clean(rel)
	if cached, ok := r.files[rel]; ok {
		return cached, nil
	}
	abs := filepath.Join(r.RepoPath, rel)
	content, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	file, diags := r.parser.ParseHCL(content, abs)
	if diags.HasErrors() {
		return nil, fmt.Errorf("parse %s: %s", rel, diags.Error())
	}
	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		return nil, fmt.Errorf("unsupported HCL body type for %s", rel)
	}
	parsed := &parsedFile{rel: rel, abs: abs, body: body}
	r.files[rel] = parsed
	return parsed, nil
}

func looksLikeReferenceAttribute(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "path") ||
		strings.Contains(lower, "file") ||
		strings.Contains(lower, "source") ||
		strings.Contains(lower, "chart") ||
		strings.Contains(lower, "values") ||
		strings.Contains(lower, "template")
}

func resolveRepoLocalPath(repoPath, baseDir, raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "://") || strings.HasPrefix(raw, "git::") || strings.HasPrefix(raw, "git@") || strings.HasPrefix(raw, "tfr://") {
		return nil
	}
	candidates := []string{}
	if filepath.IsAbs(raw) {
		if rel, err := filepath.Rel(repoPath, raw); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			candidates = append(candidates, rel)
		}
	} else {
		candidates = append(candidates, filepath.Clean(filepath.Join(baseDir, raw)))
		if !strings.HasPrefix(raw, ".") && !strings.HasPrefix(raw, "..") {
			candidates = append(candidates, filepath.Clean(raw))
		}
	}
	seen := map[string]struct{}{}
	var out []string
	for _, candidate := range candidates {
		candidate = filepath.ToSlash(candidate)
		if _, err := os.Stat(filepath.Join(repoPath, candidate)); err == nil {
			if _, ok := seen[candidate]; !ok {
				seen[candidate] = struct{}{}
				out = append(out, candidate)
			}
			continue
		}
		if info, err := os.Stat(filepath.Join(repoPath, candidate)); err == nil && info.IsDir() {
			if _, ok := seen[candidate]; !ok {
				seen[candidate] = struct{}{}
				out = append(out, candidate)
			}
		}
	}
	sort.Strings(out)
	return out
}

func objectValue(values map[string]cty.Value) cty.Value {
	if len(values) == 0 {
		return cty.EmptyObjectVal
	}
	return cty.ObjectVal(values)
}

func normalizeObjectLike(value cty.Value) cty.Value {
	if !value.IsKnown() || value.IsNull() {
		return cty.EmptyObjectVal
	}
	switch {
	case value.Type().IsObjectType():
		return value
	case value.Type().IsMapType():
		m := map[string]cty.Value{}
		it := value.ElementIterator()
		for it.Next() {
			key, elem := it.Element()
			m[key.AsString()] = elem
		}
		return objectValue(m)
	default:
		return cty.EmptyObjectVal
	}
}

func flattenObjectLike(value cty.Value) map[string]cty.Value {
	out := map[string]cty.Value{}
	if !value.IsKnown() || value.IsNull() {
		return out
	}
	switch {
	case value.Type().IsObjectType(), value.Type().IsMapType():
		it := value.ElementIterator()
		for it.Next() {
			key, elem := it.Element()
			out[key.AsString()] = elem
		}
	}
	return out
}

func simpleNoArgStringFunc(fn func() (string, error)) function.Function {
	return function.New(&function.Spec{
		Type: function.StaticReturnType(cty.String),
		Impl: func(args []cty.Value, ret cty.Type) (cty.Value, error) {
			v, err := fn()
			if err != nil {
				return cty.DynamicVal, err
			}
			return cty.StringVal(v), nil
		},
	})
}

func simpleUnaryStringFunc(fn func(string) string) function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{{Type: cty.String}},
		Type:   function.StaticReturnType(cty.String),
		Impl: func(args []cty.Value, ret cty.Type) (cty.Value, error) {
			return cty.StringVal(fn(args[0].AsString())), nil
		},
	})
}

func findInParentFolders(startDir, name string) (string, error) {
	current := filepath.Clean(startDir)
	for {
		candidate := filepath.Join(current, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("could not find %s in parent folders", name)
		}
		current = parent
	}
}
