package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/engine"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/kube"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/notify"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/render"
	"github.com/gurgenyegoryan/kube-ops-copilot/internal/report"
	"github.com/spf13/cobra"
)

type diagnoseFlags struct {
	Kubeconfig              string
	Context                 string
	Output                  string
	Timeout                 time.Duration
	EventsSince             time.Duration
	IncludeSystemNamespaces bool
	Notify                  bool
}

func NewDiagnoseCmd() *cobra.Command {
	f := diagnoseFlags{}
	cmd := &cobra.Command{
		Use:   "diagnose",
		Short: "Analyze the cluster and produce an operator-grade reliability report",
		RunE: func(cmd *cobra.Command, args []string) error {
			progress := newLiveProgress(cmd.OutOrStdout(), "diagnose")
			defer progress.Close()
			ctx, cancel := context.WithTimeout(cmd.Context(), f.Timeout)
			defer cancel()

			progress.Updatef("connecting to cluster")
			client, err := kube.NewClient(kube.Config{Kubeconfig: f.Kubeconfig, Context: f.Context})
			if err != nil {
				progress.Failf("connecting to cluster")
				return err
			}

			progress.Updatef("running analyzers")
			e := engine.Engine{Analyzers: defaultAnalyzers(ctx, client.Kubernetes, f.IncludeSystemNamespaces, f.EventsSince), Progress: progress.Eventf}
			results, err := e.Run(ctx)
			if err != nil {
				progress.Failf("running analyzers")
				return err
			}
			if wr := warningResult(client.WarningCollector.Snapshot()); len(wr.Findings) > 0 || len(wr.Evidence) > 0 || len(wr.HiddenRisks) > 0 || len(wr.Recommended.ShortTerm) > 0 {
				results = append(results, wr)
			}

			progress.Updatef("building report")
			rep := report.Build(results)
			progress.Close()
			if err := render.Write(cmd.OutOrStdout(), render.Format(f.Output), rep); err != nil {
				return err
			}

			if f.Notify {
				progress = newLiveProgress(cmd.OutOrStdout(), "diagnose")
				defer progress.Close()
				progress.Updatef("sending notification")
				n := notify.NewFromConfig(notify.FromEnv())
				if n == nil {
					progress.Failf("sending notification")
					return fmt.Errorf("--notify set but no notifier configured; set KUBE_OPS_COPILOT_SLACK_WEBHOOK_URL and/or KUBE_OPS_COPILOT_TELEGRAM_BOT_TOKEN + KUBE_OPS_COPILOT_TELEGRAM_CHAT_ID")
				}
				body := strings.TrimSpace(rep.ExecutiveSummary)
				if body == "" {
					body = fmt.Sprintf("findings=%d", len(rep.KeyFindings))
				}
				_ = n.Send(ctx, notify.Message{Title: "kube-ops-copilot diagnose", Body: body})
				progress.Donef("notification sent")
				return nil
			}
			progress.Donef("report ready")
			return nil
		},
	}

	cmd.Flags().StringVar(&f.Kubeconfig, "kubeconfig", "", "Path to kubeconfig (default: in-cluster; else $KUBECONFIG; else ~/.kube/config)")
	cmd.Flags().StringVar(&f.Context, "context", "", "Kubeconfig context override (default: current-context)")
	cmd.Flags().StringVar(&f.Output, "output", string(render.FormatMarkdown), "Output format: markdown|json")
	cmd.Flags().DurationVar(&f.Timeout, "timeout", 30*time.Second, "Overall diagnose timeout")
	cmd.Flags().DurationVar(&f.EventsSince, "events-since", 60*time.Minute, "How far back to analyze Warning events")
	cmd.Flags().BoolVar(&f.IncludeSystemNamespaces, "include-system-namespaces", false, "Include kube-system and other system namespaces in workload/resource/policy checks")
	cmd.Flags().BoolVar(&f.Notify, "notify", false, "Send a notification (Slack/Telegram via env vars)")

	return cmd
}
