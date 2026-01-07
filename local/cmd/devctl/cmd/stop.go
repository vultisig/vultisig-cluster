package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func NewStopCmd() *cobra.Command {
	var keepInfra bool
	var clean bool

	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop all local development services",
		Long: `Stop all local development services gracefully.

This command:
1. Stops Go services by PID files
2. Kills any orphaned go run processes
3. Releases ports (8080, 8082, 8089, 8181, 8183-8187)
4. Stops Docker infrastructure (unless --keep-infra)

With --clean flag:
- Removes Docker volumes (clears PostgreSQL, Redis, MinIO data)
- Removes local vault cache (~/.vultisig/vaults/)
- Keeps the original imported vault file intact
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStopWithReport(keepInfra, clean)
		},
	}

	cmd.Flags().BoolVar(&keepInfra, "keep-infra", false, "Keep Docker infrastructure running")
	cmd.Flags().BoolVar(&clean, "clean", false, "Clean all data (databases, MinIO, local vault cache)")

	return cmd
}

func runStop() {
	// Stop Go services by PID files
	pidFiles := []string{
		"/tmp/relay.pid",
		"/tmp/vultiserver.pid",
		"/tmp/vultiserver-worker.pid",
		"/tmp/verifier.pid",
		"/tmp/worker.pid",
		"/tmp/dca.pid",
		"/tmp/dca-worker.pid",
		"/tmp/dca-scheduler.pid",
		"/tmp/dca-tx-indexer.pid",
	}

	for _, pidFile := range pidFiles {
		if data, err := os.ReadFile(pidFile); err == nil {
			pid := strings.TrimSpace(string(data))
			if pidInt, err := strconv.Atoi(pid); err == nil {
				exec.Command("kill", "-9", strconv.Itoa(pidInt)).Run()
			}
			os.Remove(pidFile)
		}
	}

	// Kill orphaned go run processes
	exec.Command("pkill", "-9", "-f", "go run.*verifier").Run()
	exec.Command("pkill", "-9", "-f", "go run.*app-recurring").Run()
	exec.Command("pkill", "-9", "-f", "go run.*vultisig-relay").Run()
	exec.Command("pkill", "-9", "-f", "go run.*vultiserver").Run()
	exec.Command("pkill", "-9", "-f", "go-build.*main").Run()

	// Release ports (including 8081 for vultiserver and 8090 for relay)
	ports := []string{"8080", "8081", "8082", "8089", "8090", "8181", "8183", "8184", "8185", "8186", "8187"}
	for _, port := range ports {
		cmd := exec.Command("lsof", "-ti:"+port)
		if out, err := cmd.Output(); err == nil {
			pids := strings.Fields(strings.TrimSpace(string(out)))
			for _, pid := range pids {
				exec.Command("kill", "-9", pid).Run()
			}
		}
	}

	// Stop Docker
	verifierRoot := findVerifierRoot()
	if verifierRoot != "" {
		composeFile := filepath.Join(verifierRoot, "devenv", "docker-compose.yaml")
		cmd := exec.Command("docker", "compose", "-f", composeFile, "down")
		cmd.Run()
	}
}

func runStopWithReport(keepInfra bool, clean bool) error {
	startTime := time.Now()

	fmt.Println("============================================")
	if clean {
		fmt.Println("  Stopping All Vultisig Services (with clean)")
	} else {
		fmt.Println("  Stopping All Vultisig Services")
	}
	fmt.Println("============================================")

	var stoppedServices []string
	var stoppedPIDs []string
	var releasedPorts []string

	// Stop Go services by PID files
	fmt.Println()
	fmt.Printf("%sStopping services by PID...%s\n", colorYellow, colorReset)

	pidFiles := map[string]string{
		"/tmp/relay.pid":              "relay",
		"/tmp/vultiserver.pid":        "vultiserver",
		"/tmp/vultiserver-worker.pid": "vultiserver-worker",
		"/tmp/verifier.pid":           "verifier",
		"/tmp/worker.pid":             "worker",
		"/tmp/dca.pid":                "dca",
		"/tmp/dca-worker.pid":         "dca-worker",
		"/tmp/dca-scheduler.pid":      "dca-scheduler",
		"/tmp/dca-tx-indexer.pid":     "dca-tx-indexer",
	}

	for pidFile, serviceName := range pidFiles {
		if data, err := os.ReadFile(pidFile); err == nil {
			pid := strings.TrimSpace(string(data))
			if pidInt, err := strconv.Atoi(pid); err == nil {
				// Check if process exists
				if exec.Command("kill", "-0", pid).Run() == nil {
					fmt.Printf("  Stopping %s (PID %s)...\n", serviceName, pid)
					exec.Command("kill", strconv.Itoa(pidInt)).Run()
					stoppedServices = append(stoppedServices, serviceName)
					stoppedPIDs = append(stoppedPIDs, pid)
				}
			}
			os.Remove(pidFile)
		}
	}

	// Kill orphaned go run processes
	fmt.Println()
	fmt.Printf("%sKilling orphaned processes...%s\n", colorYellow, colorReset)
	exec.Command("pkill", "-9", "-f", "go run.*verifier").Run()
	exec.Command("pkill", "-9", "-f", "go run.*app-recurring").Run()
	exec.Command("pkill", "-9", "-f", "go run.*vultisig-relay").Run()
	exec.Command("pkill", "-9", "-f", "go run.*vultiserver").Run()
	exec.Command("pkill", "-9", "-f", "go-build.*main").Run()

	// Release ports (including 8081 for vultiserver and 8090 for relay)
	fmt.Printf("%sReleasing ports...%s\n", colorYellow, colorReset)
	ports := []string{"8080", "8081", "8082", "8089", "8090", "8181", "8183", "8184", "8185", "8186", "8187"}
	for _, port := range ports {
		cmd := exec.Command("lsof", "-ti:" + port)
		if out, err := cmd.Output(); err == nil && len(out) > 0 {
			pids := strings.Fields(strings.TrimSpace(string(out)))
			for _, pid := range pids {
				exec.Command("kill", "-9", pid).Run()
			}
			releasedPorts = append(releasedPorts, port)
		}
	}

	// Stop Docker
	stoppedContainers := 0
	volumesRemoved := false
	if !keepInfra {
		fmt.Println()
		if clean {
			fmt.Printf("%sStopping Docker containers and removing volumes...%s\n", colorYellow, colorReset)
		} else {
			fmt.Printf("%sStopping Docker containers...%s\n", colorYellow, colorReset)
		}
		verifierRoot := findVerifierRoot()
		if verifierRoot != "" {
			composeFile := filepath.Join(verifierRoot, "devenv", "docker-compose.yaml")

			// Count running containers
			cmd := exec.Command("docker", "compose", "-f", composeFile, "ps", "-q")
			if out, err := cmd.Output(); err == nil {
				stoppedContainers = len(strings.Fields(string(out)))
			}

			if clean {
				// Use -v flag to remove volumes (clears all data)
				cmd = exec.Command("docker", "compose", "-f", composeFile, "down", "-v")
				volumesRemoved = true
			} else {
				cmd = exec.Command("docker", "compose", "-f", composeFile, "down")
			}
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Run()
		}
	} else {
		fmt.Println()
		fmt.Printf("%sKeeping Docker infrastructure running%s\n", colorYellow, colorReset)
	}

	// Clean local vault cache if requested
	vaultsCleaned := 0
	if clean {
		fmt.Println()
		fmt.Printf("%sCleaning local vault cache...%s\n", colorYellow, colorReset)
		homeDir, err := os.UserHomeDir()
		if err == nil {
			vaultsDir := filepath.Join(homeDir, ".vultisig", "vaults")
			if entries, err := os.ReadDir(vaultsDir); err == nil {
				for _, entry := range entries {
					if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".vult") {
						vaultPath := filepath.Join(vaultsDir, entry.Name())
						if err := os.Remove(vaultPath); err == nil {
							fmt.Printf("  Removed: %s\n", entry.Name())
							vaultsCleaned++
						}
					}
				}
			}
		}
		if vaultsCleaned == 0 {
			fmt.Println("  No cached vaults to clean")
		}
	}

	// Print summary
	elapsed := time.Since(startTime)

	fmt.Println()
	fmt.Printf("%s┌─────────────────────────────────────────────────────────────────┐%s\n", colorCyan, colorReset)
	fmt.Printf("%s│%s %sSHUTDOWN COMPLETE%s                                               %s│%s\n", colorCyan, colorReset, colorBold, colorReset, colorCyan, colorReset)
	fmt.Printf("%s├─────────────────────────────────────────────────────────────────┤%s\n", colorCyan, colorReset)
	fmt.Printf("%s│%s                                                                 %s│%s\n", colorCyan, colorReset, colorCyan, colorReset)
	fmt.Printf("%s│%s  Services stopped:      %-5d                                   %s│%s\n", colorCyan, colorReset, len(stoppedServices), colorCyan, colorReset)

	for i, svc := range stoppedServices {
		pid := ""
		if i < len(stoppedPIDs) {
			pid = stoppedPIDs[i]
		}
		fmt.Printf("%s│%s    %-20s (was PID %s)                       %s│%s\n", colorCyan, colorReset, svc, pid, colorCyan, colorReset)
	}

	fmt.Printf("%s│%s                                                                 %s│%s\n", colorCyan, colorReset, colorCyan, colorReset)
	fmt.Printf("%s│%s  Ports released:        %-5d                                   %s│%s\n", colorCyan, colorReset, len(releasedPorts), colorCyan, colorReset)
	fmt.Printf("%s│%s  Containers stopped:    %-5d                                   %s│%s\n", colorCyan, colorReset, stoppedContainers, colorCyan, colorReset)
	if clean {
		fmt.Printf("%s│%s  Volumes removed:       %-5v                                   %s│%s\n", colorCyan, colorReset, volumesRemoved, colorCyan, colorReset)
		fmt.Printf("%s│%s  Vaults cleaned:        %-5d                                   %s│%s\n", colorCyan, colorReset, vaultsCleaned, colorCyan, colorReset)
	}
	fmt.Printf("%s│%s                                                                 %s│%s\n", colorCyan, colorReset, colorCyan, colorReset)
	fmt.Printf("%s│%s  Total shutdown time:   %s%ds%s                                       %s│%s\n", colorCyan, colorReset, colorBold, int(elapsed.Seconds()), colorReset, colorCyan, colorReset)
	fmt.Printf("%s│%s                                                                 %s│%s\n", colorCyan, colorReset, colorCyan, colorReset)
	fmt.Printf("%s└─────────────────────────────────────────────────────────────────┘%s\n", colorCyan, colorReset)
	fmt.Println()

	return nil
}
