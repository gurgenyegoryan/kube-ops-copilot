package cli

import (
	"net/http"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/approval"
)

func newApprovalHTTPServer(addr string, slack approval.Slack) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/slack/actions", slack)
	return &http.Server{Addr: addr, Handler: mux}
}
