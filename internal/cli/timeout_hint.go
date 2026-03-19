package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

func withLLMTimeoutHint(err error, command string, timeout time.Duration) error {
	if err == nil {
		return nil
	}
	if !isTimeoutErr(err) {
		return err
	}
	suggested := suggestedTimeout(timeout)
	return fmt.Errorf("%w\nhint: %s hit its timeout (%s) before the LLM response completed; try rerunning with a larger timeout, for example --timeout %s", err, command, timeout, suggested)
}

func isTimeoutErr(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "context deadline exceeded")
}

func suggestedTimeout(current time.Duration) time.Duration {
	if current < 10*time.Minute {
		return 10 * time.Minute
	}
	if current < 20*time.Minute {
		return 20 * time.Minute
	}
	return current + 10*time.Minute
}
