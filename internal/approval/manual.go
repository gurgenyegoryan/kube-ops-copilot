package approval

import (
	"context"
	"fmt"
	"time"
)

type Manual struct{}

func (m Manual) Provider() Provider { return ProviderManual }

func (m Manual) Status(ctx context.Context, approvalID string) (Status, error) {
	_ = ctx
	if approvalID == "" {
		return Status{}, fmt.Errorf("approval id is required")
	}
	return Status{Decision: DecisionApproved, Approver: "operator", ObservedAt: time.Now().UTC(), Reason: "manual approval via CLI flags"}, nil
}

func (m Manual) Request(ctx context.Context, req Request) (string, error) {
	_ = ctx
	if req.ApprovalID == "" {
		return "", fmt.Errorf("approval id is required")
	}
	return req.ApprovalID, nil
}
