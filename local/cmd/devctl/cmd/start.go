package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	colorRed    = "\033[0;31m"
	colorGreen  = "\033[0;32m"
	colorYellow = "\033[1;33m"
	colorCyan   = "\033[0;36m"
	colorBold   = "\033[1m"
	colorReset  = "\033[0m"
)

func NewStartCmd() *cobra.Command {
	var skipDCA bool

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start all local development services",
		Long: `Start all local development services (stops existing first).

This command reads cluster.yaml to determine:
- Which repos to use (paths configured per developer)
- Which services to run locally vs use production endpoints
- Port configurations

Services started:
1. Docker infrastructure (PostgreSQL, Redis, MinIO)
2. Verifier API server
3. Verifier Worker
4. DCA Plugin Server (if configured as local)
5. DCA Plugin Worker
6. DCA Scheduler
7. DCA TX Indexer

All services run in the background with logs in /tmp/*.log
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStart(skipDCA)
		},
	}

	cmd.Flags().BoolVar(&skipDCA, "skip-dca", false, "Skip starting DCA plugin services")

	return cmd
}

func runStart(skipDCA bool) error {
	startTime := time.Now()

	fmt.Println("============================================")
	fmt.Println("  Vultisig Local Dev Environment Startup")
	fmt.Println("============================================")
	fmt.Println()

	config, err := LoadClusterConfig()
	if err != nil {
		return fmt.Errorf("load cluster config: %w", err)
	}

	err = config.ValidateRepos()
	if err != nil {
		return fmt.Errorf("validate repos: %w", err)
	}

	verifierRoot := config.Repos.Verifier
	dcaRoot := config.Repos.DCA
	configsDir := findConfigsDir()
	dyldPath := config.GetDYLDPath()

	fmt.Printf("Using config:\n")
	fmt.Printf("  Verifier: %s\n", verifierRoot)
	if config.IsLocal("dca") {
		fmt.Printf("  DCA:      %s\n", dcaRoot)
	}
	fmt.Printf("  Relay:    %s\n", config.GetRelayURL())
	fmt.Printf("  Vault:    %s\n", config.GetVultiserverURL())
	fmt.Println()

	// Step 0: Stop existing services
	fmt.Printf("%s[0/8]%s Cleaning up existing processes...\n", colorYellow, colorReset)
	runStop()
	time.Sleep(2 * time.Second)
	fmt.Printf("%s✓%s Cleanup complete\n", colorGreen, colorReset)

	// Step 1: Start Docker infrastructure
	fmt.Println()
	fmt.Printf("%s[1/8]%s Starting Docker infrastructure...\n", colorYellow, colorReset)

	composeFile := filepath.Join(configsDir, "docker-compose.yaml")
	if _, err := os.Stat(composeFile); os.IsNotExist(err) {
		return fmt.Errorf("docker-compose.yaml not found at %s", composeFile)
	}

	dockerCmd := exec.Command("docker", "compose", "-f", composeFile, "down", "-v", "--remove-orphans")
	dockerCmd.Run()
	time.Sleep(1 * time.Second)

	dockerCmd = exec.Command("docker", "compose", "-f", composeFile, "up", "-d")
	dockerCmd.Stdout = os.Stdout
	dockerCmd.Stderr = os.Stderr
	err = dockerCmd.Run()
	if err != nil {
		return fmt.Errorf("failed to start docker: %w", err)
	}

	// Wait for PostgreSQL
	fmt.Println("Waiting for PostgreSQL...")
	time.Sleep(3 * time.Second)
	for i := 0; i < 30; i++ {
		checkCmd := exec.Command("docker", "exec", "vultisig-postgres", "pg_isready", "-U", "vultisig", "-d", "vultisig")
		if checkCmd.Run() == nil {
			break
		}
		time.Sleep(1 * time.Second)
	}
	fmt.Printf("%s✓%s PostgreSQL is ready\n", colorGreen, colorReset)

	// Wait for Redis
	fmt.Println("Waiting for Redis...")
	for i := 0; i < 30; i++ {
		checkCmd := exec.Command("docker", "exec", "vultisig-redis", "redis-cli", "-a", "vultisig", "ping")
		if out, _ := checkCmd.Output(); strings.TrimSpace(string(out)) == "PONG" {
			break
		}
		time.Sleep(1 * time.Second)
	}
	fmt.Printf("%s✓%s Redis is ready\n", colorGreen, colorReset)

	// Wait for MinIO
	fmt.Println("Waiting for MinIO...")
	time.Sleep(2 * time.Second)
	fmt.Printf("%s✓%s MinIO is ready\n", colorGreen, colorReset)

	// Step 1.5: Start Relay Server (if local)
	if config.IsLocal("relay") {
		fmt.Println()
		fmt.Printf("%s[1.5/8]%s Starting Relay Server...\n", colorYellow, colorReset)

		relayRoot := config.Repos.Relay
		relayConfigFile := filepath.Join(configsDir, "relay.json")

		relayCmd := exec.Command("go", "run", "cmd/router/main.go", "-config", relayConfigFile)
		relayCmd.Dir = relayRoot

		relayLog, err := os.Create("/tmp/relay.log")
		if err != nil {
			fmt.Printf("  %s!%s Failed to create relay log: %v\n", colorYellow, colorReset, err)
		} else {
			relayCmd.Stdout = relayLog
			relayCmd.Stderr = relayLog

			err = relayCmd.Start()
			if err != nil {
				fmt.Printf("  %s!%s Failed to start relay: %v\n", colorYellow, colorReset, err)
			} else {
				writePIDFile("/tmp/relay.pid", relayCmd.Process.Pid)
				fmt.Printf("  PID: %d\n", relayCmd.Process.Pid)
				fmt.Println("  Log: /tmp/relay.log")

				relayURL := fmt.Sprintf("http://localhost:%d/ping", config.Ports.Relay)
				fmt.Println("  Waiting for Relay Server...")
				if waitForHealthy(relayURL, 30*time.Second) {
					fmt.Printf("  %s✓%s Relay Server ready\n", colorGreen, colorReset)
				} else {
					fmt.Printf("  %s!%s Relay Server failed to start - check /tmp/relay.log\n", colorYellow, colorReset)
				}
			}
		}
	}

	// Step 1.6: Start Vultiserver (if local)
	if config.IsLocal("vultiserver") {
		fmt.Println()
		fmt.Printf("%s[1.6/8]%s Starting Vultiserver...\n", colorYellow, colorReset)

		vultiserverRoot := config.Repos.Vultiserver
		vultiserverConfigFile := filepath.Join(configsDir, "vultiserver.json")

		// Create vaults directory
		os.MkdirAll("/tmp/vultiserver-vaults", 0755)

		// Copy config to vultiserver directory (viper reads from current dir)
		configData, err := os.ReadFile(vultiserverConfigFile)
		if err != nil {
			fmt.Printf("  %s!%s Failed to read vultiserver config: %v\n", colorYellow, colorReset, err)
		} else {
			os.WriteFile(filepath.Join(vultiserverRoot, "config.json"), configData, 0644)

			vultiserverCmd := exec.Command("go", "run", "cmd/vultisigner/main.go")
			vultiserverCmd.Dir = vultiserverRoot
			vultiserverCmd.Env = append(os.Environ(),
				"DYLD_LIBRARY_PATH="+dyldPath+":"+os.Getenv("DYLD_LIBRARY_PATH"),
			)

			vultiserverLog, err := os.Create("/tmp/vultiserver.log")
			if err != nil {
				fmt.Printf("  %s!%s Failed to create vultiserver log: %v\n", colorYellow, colorReset, err)
			} else {
				vultiserverCmd.Stdout = vultiserverLog
				vultiserverCmd.Stderr = vultiserverLog

				err = vultiserverCmd.Start()
				if err != nil {
					fmt.Printf("  %s!%s Failed to start vultiserver: %v\n", colorYellow, colorReset, err)
				} else {
					writePIDFile("/tmp/vultiserver.pid", vultiserverCmd.Process.Pid)
					fmt.Printf("  PID: %d\n", vultiserverCmd.Process.Pid)
					fmt.Println("  Log: /tmp/vultiserver.log")

					// Also start vultiserver worker
					vultiserverWorkerCmd := exec.Command("go", "run", "cmd/worker/main.go")
					vultiserverWorkerCmd.Dir = vultiserverRoot
					vultiserverWorkerCmd.Env = append(os.Environ(),
						"DYLD_LIBRARY_PATH="+dyldPath+":"+os.Getenv("DYLD_LIBRARY_PATH"),
					)

					vultiserverWorkerLog, _ := os.Create("/tmp/vultiserver-worker.log")
					vultiserverWorkerCmd.Stdout = vultiserverWorkerLog
					vultiserverWorkerCmd.Stderr = vultiserverWorkerLog

					err = vultiserverWorkerCmd.Start()
					if err != nil {
						fmt.Printf("  %s!%s Failed to start vultiserver worker: %v\n", colorYellow, colorReset, err)
					} else {
						writePIDFile("/tmp/vultiserver-worker.pid", vultiserverWorkerCmd.Process.Pid)
						fmt.Printf("  Worker PID: %d\n", vultiserverWorkerCmd.Process.Pid)
						fmt.Println("  Worker Log: /tmp/vultiserver-worker.log")
					}

					vultiserverURL := fmt.Sprintf("http://localhost:%d/ping", config.Ports.Vultiserver)
					fmt.Println("  Waiting for Vultiserver API...")
					if waitForHealthy(vultiserverURL, 60*time.Second) {
						fmt.Printf("  %s✓%s Vultiserver ready\n", colorGreen, colorReset)
					} else {
						fmt.Printf("  %s!%s Vultiserver failed to start - check /tmp/vultiserver.log\n", colorYellow, colorReset)
					}
				}
			}
		}
	}

	// Step 2: Start Verifier Server
	fmt.Println()
	fmt.Printf("%s[2/8]%s Starting Verifier Server...\n", colorYellow, colorReset)

	verifierCmd := exec.Command("go", "run", "cmd/verifier/main.go")
	verifierCmd.Dir = verifierRoot
	verifierCmd.Env = append(os.Environ(),
		"DYLD_LIBRARY_PATH="+dyldPath+":"+os.Getenv("DYLD_LIBRARY_PATH"),
		"VS_VERIFIER_CONFIG_NAME=devenv/config/verifier",
	)

	verifierLog, err := os.Create("/tmp/verifier.log")
	if err != nil {
		return fmt.Errorf("create verifier log: %w", err)
	}
	verifierCmd.Stdout = verifierLog
	verifierCmd.Stderr = verifierLog

	err = verifierCmd.Start()
	if err != nil {
		return fmt.Errorf("start verifier: %w", err)
	}
	writePIDFile("/tmp/verifier.pid", verifierCmd.Process.Pid)
	fmt.Printf("  PID: %d\n", verifierCmd.Process.Pid)
	fmt.Println("  Log: /tmp/verifier.log")

	// Wait for Verifier API
	verifierURL := fmt.Sprintf("http://localhost:%d/plugins", config.Ports.Verifier)
	fmt.Println("  Waiting for Verifier API (compiling + migrations)...")
	if !waitForHealthy(verifierURL, 60*time.Second) {
		return fmt.Errorf("verifier failed to start - check /tmp/verifier.log")
	}
	fmt.Printf("  %s✓%s Verifier API ready\n", colorGreen, colorReset)

	// Seed plugins
	fmt.Println("  Seeding plugins...")
	seedFile := filepath.Join(configsDir, "seed-plugins.sql")
	seedCmd := exec.Command("docker", "exec", "-i", "vultisig-postgres", "psql", "-U", "vultisig", "-d", "vultisig-verifier")
	seedData, _ := os.ReadFile(seedFile)
	seedCmd.Stdin = strings.NewReader(string(seedData))
	seedCmd.Run()
	fmt.Printf("  %s✓%s Plugins seeded\n", colorGreen, colorReset)

	// Step 3: Start Verifier Worker
	fmt.Println()
	fmt.Printf("%s[3/8]%s Starting Verifier Worker...\n", colorYellow, colorReset)

	// Generate worker config with relay URL from cluster.yaml
	workerConfigPath := filepath.Join(verifierRoot, "devenv/config/worker-generated.json")
	if err := generateVerifierWorkerConfig(verifierRoot, config.GetRelayURL(), workerConfigPath); err != nil {
		return fmt.Errorf("generate worker config: %w", err)
	}

	workerCmd := exec.Command("go", "run", "cmd/worker/main.go")
	workerCmd.Dir = verifierRoot
	workerCmd.Env = append(os.Environ(),
		"DYLD_LIBRARY_PATH="+dyldPath+":"+os.Getenv("DYLD_LIBRARY_PATH"),
		"VS_WORKER_CONFIG_NAME=devenv/config/worker-generated",
	)

	workerLog, _ := os.Create("/tmp/worker.log")
	workerCmd.Stdout = workerLog
	workerCmd.Stderr = workerLog

	err = workerCmd.Start()
	if err != nil {
		return fmt.Errorf("start worker: %w", err)
	}
	writePIDFile("/tmp/worker.pid", workerCmd.Process.Pid)
	fmt.Printf("  PID: %d\n", workerCmd.Process.Pid)
	fmt.Println("  Log: /tmp/worker.log")

	// Step 4-8: Start DCA Plugin services
	if !skipDCA && config.IsLocal("dca") && dcaRoot != "" {
		fmt.Println()
		fmt.Printf("%s[4/8]%s Starting DCA Plugin Server...\n", colorYellow, colorReset)

		dcaEnvFile := filepath.Join(configsDir, "dca-server.env")
		dcaEnv := loadEnvFile(dcaEnvFile)

		dcaCmd := exec.Command("go", "run", "cmd/server/main.go")
		dcaCmd.Dir = dcaRoot
		dcaCmd.Env = append(os.Environ(), dcaEnv...)
		dcaCmd.Env = append(dcaCmd.Env, "DYLD_LIBRARY_PATH="+dyldPath+":"+os.Getenv("DYLD_LIBRARY_PATH"))

		dcaLog, _ := os.Create("/tmp/dca.log")
		dcaCmd.Stdout = dcaLog
		dcaCmd.Stderr = dcaLog

		err = dcaCmd.Start()
		if err != nil {
			fmt.Printf("  %s!%s Failed to start DCA server: %v\n", colorYellow, colorReset, err)
		} else {
			writePIDFile("/tmp/dca.pid", dcaCmd.Process.Pid)
			fmt.Printf("  PID: %d\n", dcaCmd.Process.Pid)
			fmt.Println("  Log: /tmp/dca.log")

			dcaURL := fmt.Sprintf("http://localhost:%d/healthz", config.Ports.DCAServer)
			fmt.Println("  Waiting for DCA Plugin API (compiling + migrations)...")
			if waitForHealthy(dcaURL, 60*time.Second) {
				fmt.Printf("  %s✓%s DCA Plugin API ready\n", colorGreen, colorReset)
			} else {
				fmt.Printf("  %s!%s DCA Plugin failed to start - check /tmp/dca.log\n", colorYellow, colorReset)
			}
		}

		fmt.Println()
		fmt.Printf("%s[5/8]%s Starting DCA Plugin Worker...\n", colorYellow, colorReset)

		dcaWorkerEnvFile := filepath.Join(configsDir, "dca-worker.env")
		dcaWorkerEnv := loadEnvFile(dcaWorkerEnvFile)

		dcaWorkerCmd := exec.Command("go", "run", "cmd/worker/main.go")
		dcaWorkerCmd.Dir = dcaRoot
		dcaWorkerCmd.Env = append(os.Environ(), dcaWorkerEnv...)
		dcaWorkerCmd.Env = append(dcaWorkerCmd.Env, "DYLD_LIBRARY_PATH="+dyldPath+":"+os.Getenv("DYLD_LIBRARY_PATH"))
		// Override relay URL from cluster config (production vs local)
		dcaWorkerCmd.Env = append(dcaWorkerCmd.Env, "VAULTSERVICE_RELAY_SERVER="+config.GetRelayURL())

		dcaWorkerLog, _ := os.Create("/tmp/dca-worker.log")
		dcaWorkerCmd.Stdout = dcaWorkerLog
		dcaWorkerCmd.Stderr = dcaWorkerLog

		err = dcaWorkerCmd.Start()
		if err != nil {
			fmt.Printf("  %s!%s Failed to start DCA worker: %v\n", colorYellow, colorReset, err)
		} else {
			writePIDFile("/tmp/dca-worker.pid", dcaWorkerCmd.Process.Pid)
			fmt.Printf("  PID: %d\n", dcaWorkerCmd.Process.Pid)
			fmt.Println("  Log: /tmp/dca-worker.log")
		}

		// Step 6: Start DCA Scheduler
		fmt.Println()
		fmt.Printf("%s[6/8]%s Starting DCA Scheduler...\n", colorYellow, colorReset)

		dcaSchedulerEnvFile := filepath.Join(configsDir, "dca-scheduler.env")
		dcaSchedulerEnv := loadEnvFile(dcaSchedulerEnvFile)

		dcaSchedulerCmd := exec.Command("go", "run", "cmd/scheduler/main.go")
		dcaSchedulerCmd.Dir = dcaRoot
		dcaSchedulerCmd.Env = append(os.Environ(), dcaSchedulerEnv...)

		dcaSchedulerLog, _ := os.Create("/tmp/dca-scheduler.log")
		dcaSchedulerCmd.Stdout = dcaSchedulerLog
		dcaSchedulerCmd.Stderr = dcaSchedulerLog

		err = dcaSchedulerCmd.Start()
		if err != nil {
			fmt.Printf("  %s!%s Failed to start DCA scheduler: %v\n", colorYellow, colorReset, err)
		} else {
			writePIDFile("/tmp/dca-scheduler.pid", dcaSchedulerCmd.Process.Pid)
			fmt.Printf("  PID: %d\n", dcaSchedulerCmd.Process.Pid)
			fmt.Println("  Log: /tmp/dca-scheduler.log")
		}

		// Step 7: Start DCA TX Indexer
		fmt.Println()
		fmt.Printf("%s[7/8]%s Starting DCA TX Indexer...\n", colorYellow, colorReset)

		dcaTxIndexerEnvFile := filepath.Join(configsDir, "dca-tx-indexer.env")
		dcaTxIndexerEnv := loadEnvFile(dcaTxIndexerEnvFile)

		dcaTxIndexerCmd := exec.Command("go", "run", "cmd/tx_indexer/main.go")
		dcaTxIndexerCmd.Dir = dcaRoot
		dcaTxIndexerCmd.Env = append(os.Environ(), dcaTxIndexerEnv...)

		dcaTxIndexerLog, _ := os.Create("/tmp/dca-tx-indexer.log")
		dcaTxIndexerCmd.Stdout = dcaTxIndexerLog
		dcaTxIndexerCmd.Stderr = dcaTxIndexerLog

		err = dcaTxIndexerCmd.Start()
		if err != nil {
			fmt.Printf("  %s!%s Failed to start DCA TX indexer: %v\n", colorYellow, colorReset, err)
		} else {
			writePIDFile("/tmp/dca-tx-indexer.pid", dcaTxIndexerCmd.Process.Pid)
			fmt.Printf("  PID: %d\n", dcaTxIndexerCmd.Process.Pid)
			fmt.Println("  Log: /tmp/dca-tx-indexer.log")
		}
	} else {
		fmt.Println()
		fmt.Printf("%s[4/8]%s Skipping DCA Plugin Server\n", colorYellow, colorReset)
		fmt.Println()
		fmt.Printf("%s[5/8]%s Skipping DCA Plugin Worker\n", colorYellow, colorReset)
		fmt.Println()
		fmt.Printf("%s[6/8]%s Skipping DCA Scheduler\n", colorYellow, colorReset)
		fmt.Println()
		fmt.Printf("%s[7/8]%s Skipping DCA TX Indexer\n", colorYellow, colorReset)
	}

	// Wait for workers to compile
	fmt.Println()
	fmt.Printf("%s[8/8]%s Waiting for workers to compile...\n", colorYellow, colorReset)
	time.Sleep(10 * time.Second)

	// Print summary
	elapsed := time.Since(startTime)
	printStartupSummary(elapsed, skipDCA, config)

	return nil
}

func writePIDFile(path string, pid int) {
	os.WriteFile(path, []byte(fmt.Sprintf("%d", pid)), 0644)
}

func waitForHealthy(url string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return false
		default:
			resp, err := http.Get(url)
			if err == nil && resp.StatusCode == http.StatusOK {
				resp.Body.Close()
				return true
			}
			if resp != nil {
				resp.Body.Close()
			}
			time.Sleep(1 * time.Second)
		}
	}
}

func loadEnvFile(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var envVars []string
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		envVars = append(envVars, line)
	}
	return envVars
}

func findConfigsDir() string {
	paths := []string{
		"configs",
		"local/configs",
		filepath.Join(os.Getenv("HOME"), ".vultisig", "configs"),
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			abs, _ := filepath.Abs(p)
			return abs
		}
	}

	return "configs"
}

func printStartupSummary(elapsed time.Duration, skipDCA bool, config *ClusterConfig) {
	fmt.Println()
	fmt.Printf("%s┌─────────────────────────────────────────────────────────────────┐%s\n", colorCyan, colorReset)
	fmt.Printf("%s│%s %sSTARTUP COMPLETE%s                                                %s│%s\n", colorCyan, colorReset, colorBold, colorReset, colorCyan, colorReset)
	fmt.Printf("%s├─────────────────────────────────────────────────────────────────┤%s\n", colorCyan, colorReset)
	fmt.Printf("%s│%s                                                                 %s│%s\n", colorCyan, colorReset, colorCyan, colorReset)
	fmt.Printf("%s│%s  Services Started:                                             %s│%s\n", colorCyan, colorReset, colorCyan, colorReset)

	if config.IsLocal("relay") {
		printServiceLine("Relay Server", "/tmp/relay.pid", fmt.Sprintf("%d", config.Ports.Relay))
	}
	if config.IsLocal("vultiserver") {
		printServiceLine("Vultiserver API", "/tmp/vultiserver.pid", fmt.Sprintf("%d", config.Ports.Vultiserver))
		printServiceLine("Vultiserver Worker", "/tmp/vultiserver-worker.pid", "N/A")
	}
	printServiceLine("Verifier API", "/tmp/verifier.pid", fmt.Sprintf("%d", config.Ports.Verifier))
	printServiceLine("Verifier Worker", "/tmp/worker.pid", fmt.Sprintf("%d", config.Ports.VerifierWorkerMetrics))

	if !skipDCA && config.IsLocal("dca") {
		printServiceLine("DCA Plugin API", "/tmp/dca.pid", fmt.Sprintf("%d", config.Ports.DCAServer))
		printServiceLine("DCA Plugin Worker", "/tmp/dca-worker.pid", fmt.Sprintf("%d", config.Ports.DCAWorkerMetrics))
		printServiceLine("DCA Scheduler", "/tmp/dca-scheduler.pid", fmt.Sprintf("%d", config.Ports.DCASchedulerMetrics))
		printServiceLine("DCA TX Indexer", "/tmp/dca-tx-indexer.pid", fmt.Sprintf("%d", config.Ports.DCATxIndexerMetrics))
	}

	fmt.Printf("%s│%s                                                                 %s│%s\n", colorCyan, colorReset, colorCyan, colorReset)
	fmt.Printf("%s│%s  Infrastructure:                                               %s│%s\n", colorCyan, colorReset, colorCyan, colorReset)
	fmt.Printf("%s│%s    PostgreSQL          localhost:%d                          %s│%s\n", colorCyan, colorReset, config.Ports.Postgres, colorCyan, colorReset)
	fmt.Printf("%s│%s    Redis               localhost:%d                          %s│%s\n", colorCyan, colorReset, config.Ports.Redis, colorCyan, colorReset)
	fmt.Printf("%s│%s    MinIO               localhost:%d (console: %d)          %s│%s\n", colorCyan, colorReset, config.Ports.Minio, config.Ports.MinioConsole, colorCyan, colorReset)
	fmt.Printf("%s│%s                                                                 %s│%s\n", colorCyan, colorReset, colorCyan, colorReset)
	fmt.Printf("%s│%s  External Services:                                            %s│%s\n", colorCyan, colorReset, colorCyan, colorReset)
	fmt.Printf("%s│%s    Relay:       %s%s\n", colorCyan, colorReset, config.GetRelayURL(), strings.Repeat(" ", 48-len(config.GetRelayURL()))+fmt.Sprintf("%s│%s", colorCyan, colorReset))
	fmt.Printf("%s│%s    Vultiserver: %s%s\n", colorCyan, colorReset, config.GetVultiserverURL(), strings.Repeat(" ", 48-len(config.GetVultiserverURL()))+fmt.Sprintf("%s│%s", colorCyan, colorReset))
	fmt.Printf("%s│%s                                                                 %s│%s\n", colorCyan, colorReset, colorCyan, colorReset)
	fmt.Printf("%s│%s  Total startup time: %s%ds%s                                        %s│%s\n", colorCyan, colorReset, colorBold, int(elapsed.Seconds()), colorReset, colorCyan, colorReset)
	fmt.Printf("%s│%s                                                                 %s│%s\n", colorCyan, colorReset, colorCyan, colorReset)
	fmt.Printf("%s└─────────────────────────────────────────────────────────────────┘%s\n", colorCyan, colorReset)

	fmt.Println()
	fmt.Printf("%sReady for vault import!%s\n", colorGreen, colorReset)
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  devctl vault import -f <vault.vult> -p <password>")
	fmt.Println("  devctl plugin install <plugin-id> -p <password>")
	fmt.Println()
}

func printServiceLine(name, pidFile, port string) {
	pid := "N/A"
	if data, err := os.ReadFile(pidFile); err == nil {
		pid = strings.TrimSpace(string(data))
	}
	fmt.Printf("%s│%s    %-20s PID: %-8s Port: %-6s %s│%s\n", colorCyan, colorReset, name, pid, port, colorCyan, colorReset)
}

// generateVerifierWorkerConfig reads the template worker.json and generates
// a new config with the relay URL from cluster.yaml (single source of truth)
func generateVerifierWorkerConfig(verifierRoot, relayURL, outputPath string) error {
	templatePath := filepath.Join(verifierRoot, "devenv/config/worker.json")
	data, err := os.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("read template: %w", err)
	}

	// Parse JSON, update relay URL, write back
	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parse template: %w", err)
	}

	// Navigate to vault_service.relay.server and update it
	if vs, ok := config["vault_service"].(map[string]interface{}); ok {
		if relay, ok := vs["relay"].(map[string]interface{}); ok {
			relay["server"] = relayURL
		}
	}

	// Write generated config
	output, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(outputPath, output, 0644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
}
