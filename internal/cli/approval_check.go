package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/approval"
)

func ensureApproved(ctx context.Context, provider string, approvalID string, wait bool, waitTimeout time.Duration) error {
	cfg := approval.FromEnv()
	cfg.Provider = approval.Provider(strings.ToLower(provider))
	ap, err := approval.New(cfg)
	if err != nil {
		return err
	}

	ctxPoll := ctx
	var cancel context.CancelFunc
	if wait {
		if waitTimeout <= 0 {
			waitTimeout = 5 * time.Minute
		}
		ctxPoll, cancel = context.WithTimeout(ctx, waitTimeout)
		defer cancel()
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		st, err := ap.Status(ctxPoll, approvalID)
		if err != nil {
			return fmt.Errorf("approval status check failed: %w", err)
		}
		switch st.Decision {
		case approval.DecisionApproved:
			return nil
		case approval.DecisionDenied:
			return fmt.Errorf("approval denied (provider=%s approval-id=%s approver=%s reason=%s)", provider, approvalID, st.Approver, st.Reason)
		case approval.DecisionPending:
			if !wait {
				return fmt.Errorf("approval pending (provider=%s approval-id=%s): %s", provider, approvalID, st.Reason)
			}
			select {
			case <-ctxPoll.Done():
				if errors.Is(ctxPoll.Err(), context.DeadlineExceeded) {
					return fmt.Errorf("approval still pending after %s (provider=%s approval-id=%s)", waitTimeout, provider, approvalID)
				}
				return ctxPoll.Err()
			case <-ticker.C:
			}
		default:
			return fmt.Errorf("unknown approval decision: %q", st.Decision)
		}
	}
}
