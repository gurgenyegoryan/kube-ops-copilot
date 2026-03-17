package cli

import (
	"github.com/spf13/cobra"
)

func NewRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "kube-ops-copilot",
		Short:         "Kube Ops Copilot — approval-driven Kubernetes reliability agent",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.AddCommand(NewVersionCmd())
	cmd.AddCommand(NewDiagnoseCmd())
	cmd.AddCommand(NewSuggestCmd())
	cmd.AddCommand(NewRemediateCmd())
	cmd.AddCommand(NewTerraformPRCmd())
	cmd.AddCommand(NewApprovalCmd())
	cmd.AddCommand(NewExecuteCmd())

	return cmd
}
