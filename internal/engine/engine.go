package engine

import (
	"context"
	"fmt"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
)

type Engine struct {
	Analyzers []analyzer.Analyzer
	Progress  func(string, ...any)
}

func (e Engine) Run(ctx context.Context) ([]analyzer.Result, error) {
	var results []analyzer.Result
	total := len(e.Analyzers)
	for i, a := range e.Analyzers {
		e.progress("analyzer %d/%d: %s", i+1, total, a.Name())
		res, err := a.Run(ctx)
		if err != nil {
			return nil, fmt.Errorf("analyzer %s: %w", a.Name(), err)
		}
		e.progress("completed analyzer %d/%d: %s (findings=%d evidence=%d unknowns=%d hidden=%d)", i+1, total, a.Name(), len(res.Findings), len(res.Evidence), len(res.Unknowns), len(res.HiddenRisks))
		results = append(results, res)
	}
	return results, nil
}

func (e Engine) progress(format string, args ...any) {
	if e.Progress != nil {
		e.Progress(format, args...)
	}
}
