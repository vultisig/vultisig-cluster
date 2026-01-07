package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/vultisig/vultisig-cluster/local/cmd/devctl/cmd"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "devctl",
		Short: "Vultisig development CLI for local plugin testing",
		PersistentPreRun: func(c *cobra.Command, args []string) {
			cmd.InitTSSConfig()
		},
		Long: `devctl is a CLI tool for testing Vultisig plugins locally.

This tool allows you to skip the browser extension and plugin marketplace UI
to quickly test any plugin you want to build.

Example workflow:
  1. Start all services:
     devctl start

  2. Import your vault:
     devctl vault import --file /path/to/vault.vult --password your-password

  3. Install a plugin (4-party TSS reshare):
     devctl plugin install vultisig-dca-0000 --password your-password

  4. Create a policy:
     devctl policy create --plugin vultisig-dca-0000 --config policy.json --password your-password

  5. Check status:
     devctl report

  6. Stop all services:
     devctl stop

Commands:
  start    - Start all local development services (stops existing first)
  stop     - Stop all local development services
  vault    - Import, list, and manage vaults
  plugin   - List, install, and manage plugins
  policy   - Create and manage policies
  auth     - Authenticate with verifier using TSS keysign
  verify   - Check transaction history and service health
  report   - Show comprehensive validation report
  status   - Show quick service status
`,
	}

	rootCmd.AddCommand(cmd.NewStartCmd())
	rootCmd.AddCommand(cmd.NewStopCmd())
	rootCmd.AddCommand(cmd.NewVaultCmd())
	rootCmd.AddCommand(cmd.NewPluginCmd())
	rootCmd.AddCommand(cmd.NewPolicyCmd())
	rootCmd.AddCommand(cmd.NewServicesCmd())
	rootCmd.AddCommand(cmd.NewStatusCmd())
	rootCmd.AddCommand(cmd.NewAuthCmd())
	rootCmd.AddCommand(cmd.NewVerifyCmd())
	rootCmd.AddCommand(cmd.NewReportCmd())
	rootCmd.AddCommand(cmd.NewDevTokenCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
