package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func NewPluginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin",
		Short: "Plugin management commands",
	}

	cmd.AddCommand(newPluginListCmd())
	cmd.AddCommand(newPluginInfoCmd())
	cmd.AddCommand(newPluginInstallCmd())
	cmd.AddCommand(newPluginUninstallCmd())
	cmd.AddCommand(newPluginSpecCmd())

	return cmd
}

func newPluginListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available plugins",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPluginList()
		},
	}
}

func newPluginInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info [plugin-id]",
		Short: "Show plugin details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPluginInfo(args[0])
		},
	}
}

func newPluginInstallCmd() *cobra.Command {
	var password string

	cmd := &cobra.Command{
		Use:   "install [plugin-id]",
		Short: "Install a plugin (initiates reshare)",
		Long: `Install a plugin by initiating a TSS reshare operation.

This will:
1. Check if the plugin exists and is available
2. Initiate a reshare session to add the plugin as a signer
3. Wait for the TSS session to complete

After installation, you can create policies for the plugin.

Environment variables:
  VAULT_PASSWORD  - Fast Vault password

Note: Requires authentication. Run 'devctl vault import' first.
`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			actualPassword := password
			if envPass := os.Getenv("VAULT_PASSWORD"); envPass != "" {
				actualPassword = envPass
			}
			if actualPassword == "" {
				var err error
				actualPassword, err = promptPassword("", "Enter Fast Vault password: ")
				if err != nil {
					return err
				}
			}
			return runPluginInstall(args[0], actualPassword)
		},
	}

	cmd.Flags().StringVarP(&password, "password", "p", "", "Fast Vault password (or set VAULT_PASSWORD env var)")

	return cmd
}

func newPluginUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall [plugin-id]",
		Short: "Uninstall a plugin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPluginUninstall(args[0])
		},
	}
}

func newPluginSpecCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "spec [plugin-id]",
		Short: "Show plugin recipe specification",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPluginSpec(args[0])
		},
	}
}

func runPluginList() error {
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	fmt.Println("Fetching available plugins...")

	url := cfg.Verifier + "/plugins"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result map[string]interface{}
	json.Unmarshal(body, &result)

	if data, ok := result["data"].(map[string]interface{}); ok {
		if plugins, ok := data["plugins"].([]interface{}); ok {
			fmt.Printf("\nAvailable Plugins (%d):\n\n", len(plugins))
			for _, p := range plugins {
				plugin := p.(map[string]interface{})
				fmt.Printf("  %s\n", plugin["id"])
				fmt.Printf("    Name: %s\n", plugin["title"])
				if desc, ok := plugin["description"].(string); ok && desc != "" {
					fmt.Printf("    Description: %s\n", desc)
				}
				fmt.Println()
			}
			return nil
		}
	}

	prettyJSON, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(prettyJSON))

	return nil
}

func runPluginInfo(pluginID string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	fmt.Printf("Fetching plugin info for %s...\n\n", pluginID)

	url := fmt.Sprintf("%s/plugins/%s", cfg.Verifier, pluginID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result map[string]interface{}
	json.Unmarshal(body, &result)

	prettyJSON, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(prettyJSON))

	return nil
}

func runPluginInstall(pluginID string, password string) error {
	startTime := time.Now()

	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	authHeader, err := GetAuthHeader()
	if err != nil {
		return fmt.Errorf("authentication required: %w\n\nRun 'devctl vault import --password xxx' to authenticate first", err)
	}

	vaults, err := ListVaults()
	if err != nil || len(vaults) == 0 {
		return fmt.Errorf("no vaults found. Import a vault first: devctl vault import")
	}
	vault := vaults[0]

	fmt.Printf("Installing plugin %s...\n", pluginID)
	fmt.Printf("  Vault: %s (%s...)\n", vault.Name, vault.PublicKeyECDSA[:16])
	fmt.Printf("  Verifier: %s\n", cfg.Verifier)

	isFastVault, err := CheckFastVaultExists(vault.PublicKeyECDSA)
	if err != nil {
		fmt.Printf("  Warning: Could not check Fast Vault Server: %v\n", err)
	} else if !isFastVault {
		return fmt.Errorf("vault is not a Fast Vault. Plugin reshare requires a vault created with Fast Vault feature")
	} else {
		fmt.Println("  Fast Vault: Yes")
	}

	if password == "" {
		return fmt.Errorf("password is required for Fast Vault reshare. Use --password flag")
	}

	// Check if plugin is already installed
	dbRecord := checkPluginInstallation(pluginID, vault.PublicKeyECDSA)
	if dbRecord != "" {
		fmt.Printf("\n  Plugin %s is already installed for this vault.\n", pluginID)
		fmt.Printf("  Installed at: %s\n", dbRecord)
		fmt.Println("\n  To reinstall, first run: devctl plugin uninstall", pluginID)
		return nil
	}

	fmt.Println("\nChecking plugin availability...")
	pluginURL := fmt.Sprintf("%s/plugins/%s", cfg.Verifier, pluginID)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", pluginURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("check plugin: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("plugin not found: %s", string(body))
	}

	fmt.Println("  Plugin found!")

	fmt.Println("\nInitiating 4-party TSS reshare...")
	fmt.Println("  Parties: CLI + Fast Vault Server + Verifier + Plugin")

	tss := NewTSSService(vault.LocalPartyID)

	reshareStart := time.Now()
	reshareCtx, reshareCancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer reshareCancel()

	newVault, err := tss.ReshareWithDKLS(reshareCtx, vault, pluginID, cfg.Verifier, authHeader, password)
	if err != nil {
		return fmt.Errorf("reshare failed: %w", err)
	}
	reshareDuration := time.Since(reshareStart)

	err = SaveVault(newVault)
	if err != nil {
		return fmt.Errorf("save vault: %w", err)
	}

	totalDuration := time.Since(startTime)

	// Wait for workers to upload keyshares to MinIO
	fmt.Println("\nWaiting for keyshare uploads...")
	time.Sleep(3 * time.Second)

	// Validate storage - check MinIO buckets (with retry)
	verifierFile, verifierSize := checkMinioFileWithRetry("vultisig-verifier", pluginID, vault.PublicKeyECDSA, 3)
	dcaFile, dcaSize := checkMinioFileWithRetry("vultisig-dca", pluginID, vault.PublicKeyECDSA, 3)

	// Check database record
	dbRecord = checkPluginInstallation(pluginID, vault.PublicKeyECDSA)

	// Print completion report
	fmt.Println()
	fmt.Println("┌─────────────────────────────────────────────────────────────────┐")
	fmt.Println("│ PLUGIN INSTALL COMPLETE                                         │")
	fmt.Println("├─────────────────────────────────────────────────────────────────┤")
	fmt.Println("│                                                                 │")
	fmt.Println("│  TSS Reshare:                                                   │")
	fmt.Printf("│    Parties:   %-50s │\n", fmt.Sprintf("%d (2→4 threshold)", len(newVault.Signers)))
	for i, signer := range newVault.Signers {
		role := getSignerRole(signer, vault.LocalPartyID)
		signerDisplay := signer
		if len(signerDisplay) > 25 {
			signerDisplay = signerDisplay[:25] + ".."
		}
		fmt.Printf("│      %d. %-27s %-17s │\n", i+1, signerDisplay, role)
	}
	fmt.Printf("│    Duration: %-50s │\n", reshareDuration.Round(time.Millisecond).String())
	fmt.Println("│                                                                 │")
	fmt.Println("│  Keyshares Stored:                                              │")
	if verifierFile != "" {
		fmt.Printf("│    Verifier (MinIO): ✓ %-41s │\n", verifierSize)
	} else {
		fmt.Printf("│    Verifier (MinIO): ✗ %-41s │\n", "Not found")
	}
	if dcaFile != "" {
		fmt.Printf("│    DCA Plugin (MinIO): ✓ %-39s │\n", dcaSize)
	} else {
		fmt.Printf("│    DCA Plugin (MinIO): ✗ %-39s │\n", "Not found")
	}
	fmt.Println("│                                                                 │")
	fmt.Println("│  Database:                                                      │")
	if dbRecord != "" {
		fmt.Printf("│    plugin_installations: ✓ %-37s │\n", dbRecord)
	} else {
		fmt.Printf("│    plugin_installations: ✗ %-37s │\n", "Not found")
	}
	fmt.Println("│                                                                 │")
	fmt.Printf("│  Total Time: %-51s │\n", totalDuration.Round(time.Millisecond).String())
	fmt.Println("│                                                                 │")
	fmt.Println("└─────────────────────────────────────────────────────────────────┘")
	fmt.Println()
	fmt.Println("Next: ./devctl policy create --plugin", pluginID, "--config policy.json -p <password>")

	return nil
}

func getSignerRole(signer, localPartyID string) string {
	if signer == localPartyID {
		return "(CLI)"
	}
	if strings.HasPrefix(signer, "Server-") {
		return "(Fast Vault Server)"
	}
	if strings.HasPrefix(signer, "verifier-") {
		return "(Verifier)"
	}
	if strings.HasPrefix(signer, "dca-worker-") {
		return "(DCA Plugin)"
	}
	return ""
}

func checkMinioFileWithRetry(bucket, pluginID, publicKey string, maxRetries int) (string, string) {
	for i := 0; i < maxRetries; i++ {
		file, size := checkMinioFile(bucket, pluginID, publicKey)
		if file != "" {
			return file, size
		}
		if i < maxRetries-1 {
			time.Sleep(time.Second)
		}
	}
	return "", ""
}

func checkMinioFile(bucket, pluginID, publicKey string) (string, string) {
	fileName := fmt.Sprintf("%s-%s.vult", pluginID, publicKey)
	cmd := exec.Command("docker", "exec", "vultisig-minio",
		"mc", "ls", "--json", "local/"+bucket+"/"+fileName)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", ""
	}

	var obj struct {
		Key  string `json:"key"`
		Size int64  `json:"size"`
	}
	json.Unmarshal(output, &obj)

	if obj.Key != "" {
		size := formatBytesShort(obj.Size)
		return obj.Key, size
	}
	return "", ""
}

func formatBytesShort(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%dB", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
}

func checkPluginInstallation(pluginID, publicKey string) string {
	cmd := exec.Command("docker", "exec", "vultisig-postgres",
		"psql", "-U", "vultisig", "-d", "vultisig-verifier", "-t", "-c",
		fmt.Sprintf("SELECT installed_at FROM plugin_installations WHERE plugin_id='%s' AND public_key='%s' LIMIT 1", pluginID, publicKey))

	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	result := strings.TrimSpace(string(output))
	if result == "" {
		return ""
	}

	t, err := time.Parse("2006-01-02 15:04:05.999999-07", result)
	if err != nil {
		return result
	}
	return t.Format("2006-01-02 15:04:05")
}

func runPluginUninstall(pluginID string) error {
	startTime := time.Now()

	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if cfg.PublicKeyECDSA == "" {
		return fmt.Errorf("no vault configured. Run 'devctl vault import' first")
	}

	fmt.Printf("Uninstalling plugin %s...\n", pluginID)
	fmt.Printf("  Vault: %s\n", cfg.PublicKeyECDSA[:16]+"...")

	// Check current installation status
	dbRecord := checkPluginInstallation(pluginID, cfg.PublicKeyECDSA)
	verifierFile, _ := checkMinioFile("vultisig-verifier", pluginID, cfg.PublicKeyECDSA)
	dcaFile, _ := checkMinioFile("vultisig-dca", pluginID, cfg.PublicKeyECDSA)

	if dbRecord == "" && verifierFile == "" && dcaFile == "" {
		fmt.Println("\n  Plugin is not installed for this vault.")
		return nil
	}

	fmt.Println("\nRemoving plugin data...")

	// Remove MinIO files (verifier + plugin 2-of-4 shares)
	verifierRemoved := removeMinioFile("vultisig-verifier", pluginID, cfg.PublicKeyECDSA)
	dcaRemoved := removeMinioFile("vultisig-dca", pluginID, cfg.PublicKeyECDSA)

	// Remove database record
	dbRemoved := removePluginInstallation(pluginID, cfg.PublicKeyECDSA)

	totalDuration := time.Since(startTime)

	// Print completion report
	fmt.Println()
	fmt.Println("┌─────────────────────────────────────────────────────────────────┐")
	fmt.Println("│ PLUGIN UNINSTALL COMPLETE                                       │")
	fmt.Println("├─────────────────────────────────────────────────────────────────┤")
	fmt.Println("│                                                                 │")
	fmt.Printf("│  Plugin:    %-52s │\n", pluginID)
	fmt.Printf("│  Vault:     %-52s │\n", cfg.PublicKeyECDSA[:16]+"...")
	fmt.Println("│                                                                 │")
	fmt.Println("│  Removed:                                                       │")
	if verifierRemoved {
		fmt.Printf("│    Verifier keyshare (MinIO): ✓ %-32s │\n", "Deleted")
	} else if verifierFile != "" {
		fmt.Printf("│    Verifier keyshare (MinIO): ✗ %-32s │\n", "Failed to delete")
	} else {
		fmt.Printf("│    Verifier keyshare (MinIO): - %-32s │\n", "Not found")
	}
	if dcaRemoved {
		fmt.Printf("│    DCA Plugin keyshare (MinIO): ✓ %-30s │\n", "Deleted")
	} else if dcaFile != "" {
		fmt.Printf("│    DCA Plugin keyshare (MinIO): ✗ %-30s │\n", "Failed to delete")
	} else {
		fmt.Printf("│    DCA Plugin keyshare (MinIO): - %-30s │\n", "Not found")
	}
	if dbRemoved {
		fmt.Printf("│    Database record: ✓ %-42s │\n", "Deleted")
	} else if dbRecord != "" {
		fmt.Printf("│    Database record: ✗ %-42s │\n", "Failed to delete")
	} else {
		fmt.Printf("│    Database record: - %-42s │\n", "Not found")
	}
	fmt.Println("│                                                                 │")
	fmt.Printf("│  Total Time: %-51s │\n", totalDuration.Round(time.Millisecond).String())
	fmt.Println("│                                                                 │")
	fmt.Println("└─────────────────────────────────────────────────────────────────┘")
	fmt.Println()
	fmt.Println("Local vault unchanged (still has original 2-of-2 keyshares).")
	fmt.Println("Ready to reinstall plugin with: devctl plugin install", pluginID, "-p <password>")

	return nil
}

func removeMinioFile(bucket, pluginID, publicKey string) bool {
	fileName := fmt.Sprintf("%s-%s.vult", pluginID, publicKey)
	cmd := exec.Command("docker", "run", "--rm", "--network", "devenv_vultisig",
		"-e", "MC_HOST_minio=http://minioadmin:minioadmin@vultisig-minio:9000",
		"minio/mc", "rm", "minio/"+bucket+"/"+fileName)

	err := cmd.Run()
	return err == nil
}

func removePluginInstallation(pluginID, publicKey string) bool {
	cmd := exec.Command("docker", "exec", "vultisig-postgres",
		"psql", "-U", "vultisig", "-d", "vultisig-verifier", "-c",
		fmt.Sprintf("DELETE FROM plugin_installations WHERE plugin_id='%s' AND public_key='%s'", pluginID, publicKey))

	err := cmd.Run()
	return err == nil
}

func runPluginSpec(pluginID string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	fmt.Printf("Fetching recipe specification for %s...\n\n", pluginID)

	url := fmt.Sprintf("%s/plugins/%s/recipe-specification", cfg.Verifier, pluginID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result map[string]interface{}
	json.Unmarshal(body, &result)

	prettyJSON, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(prettyJSON))

	return nil
}

func doRequest(method, url string, body interface{}) ([]byte, int, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, 0, err
	}
	_ = cfg

	var reqBody io.Reader
	if body != nil {
		jsonBody, _ := json.Marshal(body)
		reqBody = bytes.NewReader(jsonBody)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, 0, err
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	return respBody, resp.StatusCode, nil
}
