package cmd

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	_ "github.com/lib/pq"
	"github.com/spf13/cobra"
)

func NewReportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "report",
		Short: "Show comprehensive validation report",
		Long: `Generate a detailed report showing:
- Service status (verifier, DCA plugin, workers)
- Infrastructure status (PostgreSQL, Redis, MinIO)
- Vault status (local vault, authentication token)
- Plugin installation status (database records, stored keyshares)
- Storage details (MinIO bucket contents with sizes)

This command validates that import and install operations completed successfully.
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReport()
		},
	}
}

type ReportSection struct {
	Title  string
	Status string
	Items  []ReportItem
}

type ReportItem struct {
	Label  string
	Value  string
	Status string
}

func runReport() error {
	cfg, err := LoadConfig()
	if err != nil {
		cfg = DefaultConfig()
	}

	startTime := time.Now()

	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════════════════════════╗")
	fmt.Println("║              VULTISIG DEV ENVIRONMENT REPORT                     ║")
	fmt.Println("╚══════════════════════════════════════════════════════════════════╝")
	fmt.Printf("  Generated: %s\n", startTime.Format("2006-01-02 15:04:05"))
	fmt.Println()

	printServicesSection(cfg)
	printInfrastructureSection()
	printVaultSection(cfg)
	printPluginSection(cfg)
	printStorageSection()
	printInspectionCommands()

	elapsed := time.Since(startTime)
	fmt.Println("─────────────────────────────────────────────────────────────────────")
	fmt.Printf("  Report generated in %v\n", elapsed.Round(time.Millisecond))
	fmt.Println()

	return nil
}

func printServicesSection(cfg *DevConfig) {
	fmt.Println("┌─────────────────────────────────────────────────────────────────┐")
	fmt.Println("│ SERVICES                                                        │")
	fmt.Println("├─────────────────────────────────────────────────────────────────┤")

	services := []struct {
		name    string
		url     string
		pidFile string
	}{
		{"Verifier API", cfg.Verifier + "/healthz", "/tmp/verifier.pid"},
		{"Verifier Worker", "", "/tmp/worker.pid"},
		{"DCA Plugin API", cfg.DCAPlugin + "/healthz", "/tmp/dca.pid"},
		{"DCA Plugin Worker", "", "/tmp/dca-worker.pid"},
	}

	for _, svc := range services {
		status := "DOWN"
		statusIcon := "✗"
		pid := ""

		if svc.pidFile != "" {
			pidData, err := os.ReadFile(svc.pidFile)
			if err == nil {
				pid = strings.TrimSpace(string(pidData))
				if isProcessRunning(pid) {
					status = "RUNNING"
					statusIcon = "✓"
				}
			}
		}

		if svc.url != "" && checkHealth(svc.url) {
			status = "HEALTHY"
			statusIcon = "✓"
		}

		pidInfo := ""
		if pid != "" {
			pidInfo = fmt.Sprintf(" (PID: %s)", pid)
		}

		fmt.Printf("│  %s %-20s %-10s%s\n", statusIcon, svc.name, status, pidInfo)
	}

	fmt.Println("└─────────────────────────────────────────────────────────────────┘")
	fmt.Println()
}

func printInfrastructureSection() {
	fmt.Println("┌─────────────────────────────────────────────────────────────────┐")
	fmt.Println("│ INFRASTRUCTURE                                                  │")
	fmt.Println("├─────────────────────────────────────────────────────────────────┤")

	infra := []struct {
		name      string
		checkFunc func() (bool, string)
	}{
		{"PostgreSQL", func() (bool, string) {
			db, err := sql.Open("postgres", "postgres://vultisig:vultisig@localhost:5432/vultisig-verifier?sslmode=disable")
			if err != nil {
				return false, ""
			}
			defer db.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err = db.PingContext(ctx)
			if err != nil {
				return false, ""
			}
			var version string
			db.QueryRow("SELECT version()").Scan(&version)
			if len(version) > 50 {
				version = version[:50] + "..."
			}
			return true, "localhost:5432"
		}},
		{"Redis", func() (bool, string) {
			cmd := exec.Command("docker", "exec", "vultisig-redis", "redis-cli", "-a", "vultisig", "PING")
			output, err := cmd.Output()
			if err != nil {
				return false, ""
			}
			if strings.TrimSpace(string(output)) == "PONG" {
				return true, "localhost:6379"
			}
			return false, ""
		}},
		{"MinIO", func() (bool, string) {
			resp, err := http.Get("http://localhost:9000/minio/health/live")
			if err != nil {
				return false, ""
			}
			defer resp.Body.Close()
			return resp.StatusCode == http.StatusOK, "localhost:9000 (console: 9090)"
		}},
	}

	for _, inf := range infra {
		ok, info := inf.checkFunc()
		status := "DOWN"
		statusIcon := "✗"
		if ok {
			status = "RUNNING"
			statusIcon = "✓"
		}

		infoStr := ""
		if info != "" {
			infoStr = fmt.Sprintf(" (%s)", info)
		}

		fmt.Printf("│  %s %-20s %-10s%s\n", statusIcon, inf.name, status, infoStr)
	}

	fmt.Println("└─────────────────────────────────────────────────────────────────┘")
	fmt.Println()
}

func printVaultSection(cfg *DevConfig) {
	fmt.Println("┌─────────────────────────────────────────────────────────────────┐")
	fmt.Println("│ VAULT                                                           │")
	fmt.Println("├─────────────────────────────────────────────────────────────────┤")

	if cfg.PublicKeyECDSA == "" {
		fmt.Println("│  ✗ No vault configured                                          │")
		fmt.Println("│    Run: devctl vault import -f <file> -p <password>             │")
		fmt.Println("└─────────────────────────────────────────────────────────────────┘")
		fmt.Println()
		return
	}

	vaults, err := ListVaults()
	if err != nil || len(vaults) == 0 {
		fmt.Println("│  ✗ Vault configured but file not found                          │")
		fmt.Println("└─────────────────────────────────────────────────────────────────┘")
		fmt.Println()
		return
	}

	vault := vaults[0]

	fmt.Printf("│  ✓ Name:          %-45s │\n", truncate(vault.Name, 45))
	fmt.Printf("│    ECDSA:         %-45s │\n", truncate(vault.PublicKeyECDSA, 45))
	fmt.Printf("│    EdDSA:         %-45s │\n", truncate(vault.PublicKeyEdDSA, 45))
	fmt.Printf("│    Local Party:   %-45s │\n", vault.LocalPartyID)
	fmt.Printf("│    Signers:       %-45s │\n", fmt.Sprintf("%d parties: %v", len(vault.Signers), truncateSigners(vault.Signers)))
	fmt.Printf("│    KeyShares:     %-45s │\n", fmt.Sprintf("%d shares", len(vault.KeyShares)))
	fmt.Printf("│    LibType:       %-45s │\n", fmt.Sprintf("%d (DKLS)", vault.LibType))
	fmt.Printf("│    Storage:       %-45s │\n", truncate(VaultStoragePath(), 45))

	token, err := LoadAuthToken()
	if err == nil && token.Token != "" {
		if time.Now().Before(token.ExpiresAt) {
			fmt.Printf("│  ✓ Auth Token:    %-45s │\n", "Valid until "+token.ExpiresAt.Format("2006-01-02"))
		} else {
			fmt.Printf("│  ✗ Auth Token:    %-45s │\n", "Expired")
		}
	} else {
		fmt.Printf("│  ✗ Auth Token:    %-45s │\n", "Not authenticated")
	}

	fmt.Println("└─────────────────────────────────────────────────────────────────┘")
	fmt.Println()
}

func printPluginSection(cfg *DevConfig) {
	fmt.Println("┌─────────────────────────────────────────────────────────────────┐")
	fmt.Println("│ PLUGIN INSTALLATIONS                                            │")
	fmt.Println("├─────────────────────────────────────────────────────────────────┤")

	db, err := sql.Open("postgres", "postgres://vultisig:vultisig@localhost:5432/vultisig-verifier?sslmode=disable")
	if err != nil {
		fmt.Println("│  ✗ Cannot connect to database                                   │")
		fmt.Println("└─────────────────────────────────────────────────────────────────┘")
		fmt.Println()
		return
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT plugin_id, public_key, installed_at
		FROM plugin_installations
		ORDER BY installed_at DESC
		LIMIT 5
	`)
	if err != nil {
		fmt.Printf("│  ✗ Query error: %-47s │\n", truncate(err.Error(), 47))
		fmt.Println("└─────────────────────────────────────────────────────────────────┘")
		fmt.Println()
		return
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var pluginID, publicKey string
		var installedAt time.Time
		rows.Scan(&pluginID, &publicKey, &installedAt)

		if count == 0 {
			fmt.Println("│  Database Records:                                              │")
		}
		count++

		fmt.Printf("│    ✓ %-20s %-36s │\n", pluginID, installedAt.Format("2006-01-02 15:04:05"))
		fmt.Printf("│      Public Key: %-47s │\n", truncate(publicKey, 47))
	}

	if count == 0 {
		fmt.Println("│  ✗ No plugins installed                                         │")
		fmt.Println("│    Run: devctl plugin install <plugin-id> -p <password>         │")
	}

	var tokenCount int
	db.QueryRow("SELECT COUNT(*) FROM vault_tokens WHERE revoked_at IS NULL AND expires_at > NOW()").Scan(&tokenCount)
	fmt.Println("│                                                                 │")
	fmt.Printf("│  Vault Tokens:    %-45s │\n", fmt.Sprintf("%d active", tokenCount))

	fmt.Println("└─────────────────────────────────────────────────────────────────┘")
	fmt.Println()
}

func printStorageSection() {
	fmt.Println("┌─────────────────────────────────────────────────────────────────┐")
	fmt.Println("│ MINIO STORAGE (Keyshares)                                       │")
	fmt.Println("├─────────────────────────────────────────────────────────────────┤")

	buckets := []struct {
		name   string
		bucket string
	}{
		{"Verifier", "vultisig-verifier"},
		{"DCA Plugin", "vultisig-dca"},
	}

	for _, b := range buckets {
		files, err := listMinioFiles(b.bucket)
		if err != nil {
			fmt.Printf("│  %-15s ✗ Error: %-38s │\n", b.name+":", truncate(err.Error(), 38))
			continue
		}

		if len(files) == 0 {
			fmt.Printf("│  %-15s ✗ No keyshares stored                        │\n", b.name+":")
			continue
		}

		fmt.Printf("│  %-15s ✓ %d file(s)                                      │\n", b.name+":", len(files))
		for _, f := range files {
			shortName := f.Name
			if len(shortName) > 40 {
				shortName = shortName[:20] + "..." + shortName[len(shortName)-17:]
			}
			fmt.Printf("│    %-50s %s │\n", shortName, f.Size)
		}
	}

	fmt.Println("└─────────────────────────────────────────────────────────────────┘")
	fmt.Println()
}

func printInspectionCommands() {
	fmt.Println("┌─────────────────────────────────────────────────────────────────┐")
	fmt.Println("│ INSPECTION COMMANDS                                             │")
	fmt.Println("├─────────────────────────────────────────────────────────────────┤")
	fmt.Println("│  View Logs:                                                     │")
	fmt.Println("│    tail -f /tmp/verifier.log      # Verifier server             │")
	fmt.Println("│    tail -f /tmp/worker.log        # Verifier worker             │")
	fmt.Println("│    tail -f /tmp/dca.log           # DCA plugin server           │")
	fmt.Println("│    tail -f /tmp/dca-worker.log    # DCA plugin worker           │")
	fmt.Println("│                                                                 │")
	fmt.Println("│  Database:                                                      │")
	fmt.Println("│    docker exec -it vultisig-postgres psql -U vultisig \\         │")
	fmt.Println("│      -d vultisig-verifier                                       │")
	fmt.Println("│                                                                 │")
	fmt.Println("│  Redis:                                                         │")
	fmt.Println("│    docker exec vultisig-redis redis-cli -a vultisig KEYS '*'    │")
	fmt.Println("│                                                                 │")
	fmt.Println("│  MinIO Console:                                                 │")
	fmt.Println("│    http://localhost:9090  (minioadmin/minioadmin)               │")
	fmt.Println("└─────────────────────────────────────────────────────────────────┘")
	fmt.Println()
}

type MinioFile struct {
	Name string
	Size string
}

func listMinioFiles(bucket string) ([]MinioFile, error) {
	cmd := exec.Command("docker", "exec", "vultisig-minio",
		"mc", "ls", "--json", "local/"+bucket+"/")

	output, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := strings.TrimSpace(string(output))
		if outputStr != "" {
			return nil, fmt.Errorf("%s", outputStr)
		}
		return nil, err
	}

	var files []MinioFile
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		var obj struct {
			Key  string `json:"key"`
			Size int64  `json:"size"`
		}
		err := json.Unmarshal([]byte(line), &obj)
		if err != nil {
			continue
		}
		if obj.Key != "" {
			files = append(files, MinioFile{
				Name: obj.Key,
				Size: formatBytes(obj.Size),
			})
		}
	}

	return files, nil
}

func formatBytes(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%dB", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
}

func isProcessRunning(pid string) bool {
	cmd := exec.Command("kill", "-0", pid)
	err := cmd.Run()
	return err == nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func truncateSigners(signers []string) string {
	if len(signers) == 0 {
		return "[]"
	}
	result := "["
	for i, s := range signers {
		if len(s) > 12 {
			s = s[:12] + ".."
		}
		if i > 0 {
			result += " "
		}
		result += s
		if len(result) > 40 {
			return result[:37] + "...]"
		}
	}
	return result + "]"
}
