package engine

import (
	"context"
	"fmt"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/analyzer"
)

type Engine struct {
	Analyzers []analyzer.Analyzer
}

func (e Engine) Run(ctx context.Context) ([]analyzer.Result, error) {
	var results []analyzer.Result
	for _, a := range e.Analyzers {
		res, err := a.Run(ctx)
		if err != nil {
			return nil, fmt.Errorf("analyzer %s: %w", a.Name(), err)
		}
		results = append(results, res)
	}
	return results, nil
}
