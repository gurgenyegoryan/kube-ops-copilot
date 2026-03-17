package kube

import "sync"

type WarningCollector struct {
	mu       sync.Mutex
	seen     map[string]struct{}
	messages []string
}

func NewWarningCollector() *WarningCollector {
	return &WarningCollector{seen: map[string]struct{}{}}
}

func (c *WarningCollector) HandleWarningHeader(code int, agent string, message string) {
	if c == nil || code != 299 || message == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.seen[message]; ok {
		return
	}
	c.seen[message] = struct{}{}
	c.messages = append(c.messages, message)
}

func (c *WarningCollector) Snapshot() []string {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.messages))
	copy(out, c.messages)
	return out
}
