package cmd

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/vultisig/commondata/go/vultisig/vault/v1"
	"github.com/vultisig/vultisig-go/address"
	"github.com/vultisig/vultisig-go/common"
	"google.golang.org/protobuf/proto"
)

func NewVaultCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vault",
		Short: "Vault management commands",
	}

	cmd.AddCommand(newVaultGenerateCmd())
	cmd.AddCommand(newVaultReshareCmd())
	cmd.AddCommand(newVaultKeysignCmd())
	cmd.AddCommand(newVaultInfoCmd())
	cmd.AddCommand(newVaultListCmd())
	cmd.AddCommand(newVaultImportCmd())
	cmd.AddCommand(newVaultExportCmd())
	cmd.AddCommand(newVaultUseCmd())
	cmd.AddCommand(newVaultBalanceCmd())
	cmd.AddCommand(newVaultAddressCmd())
	cmd.AddCommand(newVaultDetailsCmd())

	return cmd
}

func newVaultGenerateCmd() *cobra.Command {
	var name string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate a new vault using Fast Vault Server",
		Long: `Generate a new 2-of-2 vault using the real relay server and Fast Vault Server.

This creates a vault where:
  - Party 1: This CLI (local keyshare stored in ~/.vultisig/vaults/)
  - Party 2: Fast Vault Server (production Vultisig server)

The vault uses DKLS threshold signatures with the production relay server.

After generation, use 'vault reshare' to add verifier and plugins.
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if dryRun {
				return runVaultGenerateDryRun(name)
			}
			return runVaultGenerate(name)
		},
	}

	cmd.Flags().StringVarP(&name, "name", "n", "DevVault", "Name for the vault")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be done without executing")

	return cmd
}

func newVaultReshareCmd() *cobra.Command {
	var pluginID string
	var verifierURL string
	var password string

	cmd := &cobra.Command{
		Use:   "reshare",
		Short: "Reshare vault to add verifier and plugin",
		Long: `Reshare the current vault to add new parties.

This performs a TSS reshare operation to change from 2-of-2 to 2-of-4:
  - Current: CLI + Fast Vault Server (2-of-2)
  - After: CLI + Fast Vault Server + Verifier + Plugin (2-of-4)

The reshare maintains the same public keys but distributes new keyshares.

Example:
  devctl vault reshare --plugin vultisig-fees-feee --verifier http://localhost:8080 --password "your-password"
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultReshare(pluginID, verifierURL, password)
		},
	}

	cmd.Flags().StringVarP(&pluginID, "plugin", "p", "", "Plugin ID to add (required, e.g., vultisig-fees-feee)")
	cmd.Flags().StringVarP(&verifierURL, "verifier", "v", "http://localhost:8080", "Verifier server URL")
	cmd.Flags().StringVar(&password, "password", "", "Fast Vault password (required)")
	cmd.MarkFlagRequired("plugin")

	return cmd
}

func newVaultKeysignCmd() *cobra.Command {
	var message string
	var derivePath string
	var isEdDSA bool
	var vaultPassword string

	cmd := &cobra.Command{
		Use:   "keysign",
		Short: "Sign a message using the vault",
		Long: `Sign a message using the current vault with Fast Vault Server.

This performs a TSS keysign operation with your vault share and the Fast Vault Server.
The message should be hex-encoded (the hash to sign).

For ECDSA signing (default), provide a derive path like "m/44'/60'/0'/0/0" for Ethereum.
For EdDSA signing, use --eddsa flag (no derive path needed).

Example:
  # Sign an Ethereum transaction hash (ECDSA)
  devctl vault keysign --message "abcd1234..." --derive "m/44'/60'/0'/0/0" --password "vault-password"

  # Sign a Solana message (EdDSA)
  devctl vault keysign --message "abcd1234..." --eddsa --password "vault-password"
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultKeysign(message, derivePath, isEdDSA, vaultPassword)
		},
	}

	cmd.Flags().StringVarP(&message, "message", "m", "", "Hex-encoded message hash to sign (required)")
	cmd.Flags().StringVarP(&derivePath, "derive", "d", "m/44'/60'/0'/0/0", "BIP44 derivation path (for ECDSA)")
	cmd.Flags().BoolVar(&isEdDSA, "eddsa", false, "Use EdDSA signing (for Solana, etc.)")
	cmd.Flags().StringVarP(&vaultPassword, "password", "p", "", "Fast Vault password (required)")
	cmd.MarkFlagRequired("message")
	cmd.MarkFlagRequired("password")

	return cmd
}

func newVaultInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Show current vault information",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultInfo()
		},
	}
}

func newVaultListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all local vaults",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultList()
		},
	}
}

func newVaultImportCmd() *cobra.Command {
	var file string
	var password string
	var force bool

	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import a vault from file",
		Long: `Import an existing vault share from a .vult backup file.

The file should be a vault backup exported from the Vultisig mobile app or extension.
If the vault is encrypted, you will be prompted for the password interactively,
or you can provide it with --password (be careful with special characters in shells).

Environment variables (override flags):
  VAULT_PATH      - Path to vault file
  VAULT_PASSWORD  - Decryption password

Use --force to overwrite any existing vault (useful after plugin uninstall).

Example:
  devctl vault import --file ~/Downloads/MyVault.vult
  devctl vault import --file ~/Downloads/MyVault.vult --password "your-password"
  VAULT_PATH=/path/to/vault.vult VAULT_PASSWORD=secret devctl vault import --force
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			actualFile := file
			if envPath := os.Getenv("VAULT_PATH"); envPath != "" {
				actualFile = envPath
			}
			if actualFile == "" {
				return fmt.Errorf("vault file required: use --file or set VAULT_PATH")
			}

			actualPassword := password
			if envPass := os.Getenv("VAULT_PASSWORD"); envPass != "" {
				actualPassword = envPass
			}
			if actualPassword == "" {
				var err error
				actualPassword, err = promptPassword("", "Enter vault password (or press Enter if unencrypted): ")
				if err != nil {
					return err
				}
			}
			return runVaultImport(actualFile, actualPassword, force)
		},
	}

	cmd.Flags().StringVarP(&file, "file", "f", "", "Vault file to import (or set VAULT_PATH env var)")
	cmd.Flags().StringVarP(&password, "password", "p", "", "Decryption password (or set VAULT_PASSWORD env var)")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing vault")

	return cmd
}

func newVaultExportCmd() *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export current vault to file",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultExport(output)
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "Output file path")

	return cmd
}

func newVaultUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use [public-key-prefix]",
		Short: "Set active vault",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultUse(args[0])
		},
	}
}

func runVaultGenerate(name string) error {
	fmt.Println("=== Vault Generation ===")
	fmt.Printf("Name: %s\n", name)
	fmt.Printf("Relay Server: %s\n", RelayServer)
	fmt.Printf("Fast Vault Server: %s\n", FastVaultServer)
	fmt.Println()

	localPartyID := fmt.Sprintf("%s-%s", DefaultLocalParty, uuid.New().String()[:8])

	fmt.Printf("Local Party ID: %s\n", localPartyID)
	fmt.Println()
	fmt.Println("Starting TSS keygen with Fast Vault Server...")
	fmt.Println()

	ctx, cancel := context.WithTimeout(context.Background(), KeygenTimeout)
	defer cancel()

	tss := NewTSSService(localPartyID)
	vault, err := tss.KeygenWithDKLS(ctx, name)
	if err != nil {
		return fmt.Errorf("keygen failed: %w", err)
	}

	err = SaveVault(vault)
	if err != nil {
		return fmt.Errorf("save vault: %w", err)
	}

	cfg, _ := LoadConfig()
	cfg.VaultName = vault.Name
	cfg.PublicKeyECDSA = vault.PublicKeyECDSA
	cfg.PublicKeyEdDSA = vault.PublicKeyEdDSA
	SaveConfig(cfg)

	fmt.Println()
	fmt.Println("=== Vault Generated Successfully ===")
	fmt.Printf("Name: %s\n", vault.Name)
	fmt.Printf("Public Key (ECDSA): %s\n", vault.PublicKeyECDSA)
	fmt.Printf("Public Key (EdDSA): %s\n", vault.PublicKeyEdDSA)
	fmt.Printf("Signers: %v\n", vault.Signers)
	fmt.Printf("Saved to: %s\n", VaultStoragePath())
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1. devctl vault reshare --plugin vultisig-fees-feee  # Add fee plugin")
	fmt.Println("  2. devctl policy create --plugin vultisig-fees-feee  # Create policy")

	return nil
}

func runVaultGenerateDryRun(name string) error {
	fmt.Println("=== Vault Generation (Dry Run) ===")
	fmt.Printf("Name: %s\n", name)
	fmt.Printf("Relay Server: %s\n", RelayServer)
	fmt.Printf("Fast Vault Server: %s\n", FastVaultServer)
	fmt.Println()
	fmt.Println("Would perform:")
	fmt.Println("  1. Generate session ID and encryption keys")
	fmt.Println("  2. Register session with relay server")
	fmt.Println("  3. Request Fast Vault Server to join keygen")
	fmt.Println("  4. Run DKLS keygen protocol for ECDSA")
	fmt.Println("  5. Run DKLS keygen protocol for EdDSA")
	fmt.Println("  6. Save vault to ~/.vultisig/vaults/")
	fmt.Println()
	fmt.Println("Run without --dry-run to execute.")

	return nil
}

func runVaultReshare(pluginID string, verifierURL string, password string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if cfg.PublicKeyECDSA == "" {
		return fmt.Errorf("no vault configured. Run 'devctl vault import' first")
	}

	vault, err := LoadVault(cfg.PublicKeyECDSA[:16])
	if err != nil {
		return fmt.Errorf("load vault: %w", err)
	}

	fmt.Println("=== Vault Reshare ===")
	fmt.Printf("Vault: %s\n", vault.Name)
	if len(vault.PublicKeyECDSA) >= 32 {
		fmt.Printf("Public Key: %s...\n", vault.PublicKeyECDSA[:32])
	}
	fmt.Printf("Current Signers: %v\n", vault.Signers)
	fmt.Printf("Plugin: %s\n", pluginID)
	fmt.Printf("Verifier: %s\n", verifierURL)
	fmt.Println()

	fmt.Println("This will reshare your vault to add:")
	fmt.Println("  - Verifier worker")
	fmt.Printf("  - Plugin: %s\n", pluginID)
	fmt.Println()
	fmt.Println("Starting TSS reshare...")

	authHeader, err := GetAuthHeader()
	if err != nil {
		fmt.Println("Warning: Not authenticated. Reshare may require authentication.")
		authHeader = ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tss := NewTSSService(vault.LocalPartyID)
	newVault, err := tss.Reshare(ctx, vault, pluginID, verifierURL, authHeader, password)
	if err != nil {
		return fmt.Errorf("reshare failed: %w", err)
	}

	err = SaveVault(newVault)
	if err != nil {
		return fmt.Errorf("save vault: %w", err)
	}

	fmt.Println()
	fmt.Println("=== Reshare Completed ===")
	fmt.Printf("New Signers: %v\n", newVault.Signers)

	return nil
}

func runVaultKeysign(message, derivePath string, isEdDSA bool, vaultPassword string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if cfg.PublicKeyECDSA == "" {
		return fmt.Errorf("no vault configured. Run 'devctl vault import' first")
	}

	vault, err := LoadVault(cfg.PublicKeyECDSA[:16])
	if err != nil {
		return fmt.Errorf("load vault: %w", err)
	}

	publicKey := vault.PublicKeyECDSA
	if isEdDSA {
		publicKey = vault.PublicKeyEdDSA
		derivePath = ""
	}

	fmt.Println("=== Vault Keysign ===")
	fmt.Printf("Vault: %s\n", vault.Name)
	if len(publicKey) >= 32 {
		fmt.Printf("Public Key: %s...\n", publicKey[:32])
	}
	fmt.Printf("Message: %s\n", message)
	if !isEdDSA {
		fmt.Printf("Derive Path: %s\n", derivePath)
	}
	fmt.Printf("Signature Type: %s\n", map[bool]string{true: "EdDSA", false: "ECDSA"}[isEdDSA])
	fmt.Println()

	fmt.Println("Starting TSS keysign with Fast Vault Server...")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	tss := NewTSSService(vault.LocalPartyID)
	results, err := tss.Keysign(ctx, vault, []string{message}, derivePath, isEdDSA, vaultPassword)
	if err != nil {
		return fmt.Errorf("keysign failed: %w", err)
	}

	fmt.Println()
	fmt.Println("=== Keysign Result ===")
	for i, result := range results {
		fmt.Printf("Message %d:\n", i+1)
		fmt.Printf("  R: %s\n", result.R)
		fmt.Printf("  S: %s\n", result.S)
		fmt.Printf("  Recovery ID: %s\n", result.RecoveryID)
		fmt.Printf("  DER Signature: %s\n", result.DerSignature)
	}

	return nil
}

func runVaultInfo() error {
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	fmt.Println("=== Current Vault ===")

	if cfg.PublicKeyECDSA == "" {
		fmt.Println("No vault configured.")
		fmt.Println()
		fmt.Println("To create a vault:")
		fmt.Println("  devctl vault generate --name MyVault")
		fmt.Println()
		fmt.Println("To import a vault:")
		fmt.Println("  devctl vault import --file vault.json")
		return nil
	}

	vault, err := LoadVault(cfg.PublicKeyECDSA[:16])
	if err != nil {
		fmt.Printf("Name: %s\n", cfg.VaultName)
		fmt.Printf("Public Key (ECDSA): %s\n", cfg.PublicKeyECDSA)
		fmt.Printf("Public Key (EdDSA): %s\n", cfg.PublicKeyEdDSA)
		fmt.Println()
		fmt.Println("[Vault file not found locally - may need to import]")
		return nil
	}

	fmt.Printf("Name: %s\n", vault.Name)
	fmt.Printf("Public Key (ECDSA): %s\n", vault.PublicKeyECDSA)
	fmt.Printf("Public Key (EdDSA): %s\n", vault.PublicKeyEdDSA)
	fmt.Printf("Local Party ID: %s\n", vault.LocalPartyID)
	fmt.Printf("Signers: %v\n", vault.Signers)
	fmt.Printf("Created: %s\n", vault.CreatedAt)
	fmt.Printf("Keyshares: %d\n", len(vault.KeyShares))
	for _, ks := range vault.KeyShares {
		fmt.Printf("  - %s: %d bytes\n", ks.PubKey[:16]+"...", len(ks.Keyshare))
	}
	fmt.Printf("LibType: %d (0=GG20, 1=DKLS)\n", vault.LibType)
	if vault.ResharePrefix != "" {
		fmt.Printf("Reshare Prefix: %s\n", vault.ResharePrefix)
	}
	fmt.Println()
	fmt.Println("Storage:", VaultStoragePath())

	return nil
}

func runVaultList() error {
	vaults, err := ListVaults()
	if err != nil {
		return fmt.Errorf("list vaults: %w", err)
	}

	if len(vaults) == 0 {
		fmt.Println("No vaults found.")
		fmt.Println()
		fmt.Println("To create a vault:")
		fmt.Println("  devctl vault generate --name MyVault")
		return nil
	}

	cfg, _ := LoadConfig()

	fmt.Printf("=== Local Vaults (%d) ===\n\n", len(vaults))

	for _, v := range vaults {
		active := ""
		if cfg.PublicKeyECDSA == v.PublicKeyECDSA {
			active = " [ACTIVE]"
		}
		fmt.Printf("  %s%s\n", v.Name, active)
		fmt.Printf("    ECDSA: %s...\n", v.PublicKeyECDSA[:32])
		fmt.Printf("    Signers: %d parties\n", len(v.Signers))
		fmt.Printf("    Created: %s\n", v.CreatedAt)
		fmt.Println()
	}

	fmt.Println("Storage:", VaultStoragePath())

	return nil
}

func runVaultImport(file, password string, force bool) error {
	startTime := time.Now()

	// Check for existing vault
	existingVaults, _ := ListVaults()
	if len(existingVaults) > 0 && !force {
		existing := existingVaults[0]
		if len(existing.Signers) > 2 {
			fmt.Printf("Warning: Existing vault has %d signers (from plugin install).\n", len(existing.Signers))
			fmt.Println("Use --force to overwrite with fresh 2-party vault.")
			return fmt.Errorf("existing vault found with %d signers. Use --force to overwrite", len(existing.Signers))
		}
	}

	if force && len(existingVaults) > 0 {
		fmt.Println("Force mode: removing existing vault...")
		vaultPath := VaultStoragePath()
		os.RemoveAll(vaultPath)
		os.MkdirAll(vaultPath, 0700)
	}

	data, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	fileInfo, _ := os.Stat(file)
	fileSize := fileInfo.Size()

	var localVault LocalVault
	var format string

	// Try to parse as .vult format (base64-encoded protobuf)
	pbVault, err := common.DecryptVaultFromBackup(password, data)
	if err == nil {
		localVault = convertProtoVaultToLocal(pbVault)
		format = ".vult (protobuf)"
		fmt.Println("Detected .vult protobuf format")
	} else {
		// Fall back to JSON format
		var backup BackupVault
		jsonErr := json.Unmarshal(data, &backup)
		if jsonErr == nil && backup.Version != "" {
			localVault = backup.Vault
			format = fmt.Sprintf("iOS backup (v%s)", backup.Version)
			fmt.Printf("Detected iOS backup format (version: %s)\n", backup.Version)
		} else {
			jsonErr = json.Unmarshal(data, &localVault)
			if jsonErr != nil {
				return fmt.Errorf("parse vault file: protobuf error: %v, json error: %v", err, jsonErr)
			}
			format = "JSON"
			fmt.Println("Detected JSON format")
		}
	}

	if localVault.PublicKeyECDSA == "" {
		return fmt.Errorf("invalid vault file: missing public key")
	}

	if localVault.CreatedAt == "" {
		localVault.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	err = SaveVault(&localVault)
	if err != nil {
		return fmt.Errorf("save vault: %w", err)
	}

	cfg, _ := LoadConfig()
	cfg.VaultName = localVault.Name
	cfg.PublicKeyECDSA = localVault.PublicKeyECDSA
	cfg.PublicKeyEdDSA = localVault.PublicKeyEdDSA
	err = SaveConfig(cfg)
	if err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Println()
	fmt.Println("=== Vault Imported ===")
	fmt.Printf("Name: %s\n", localVault.Name)
	if len(localVault.PublicKeyECDSA) >= 32 {
		fmt.Printf("Public Key (ECDSA): %s...\n", localVault.PublicKeyECDSA[:32])
	} else {
		fmt.Printf("Public Key (ECDSA): %s\n", localVault.PublicKeyECDSA)
	}
	fmt.Printf("Public Key (EdDSA): %s\n", localVault.PublicKeyEdDSA)
	fmt.Printf("Local Party ID: %s\n", localVault.LocalPartyID)
	fmt.Printf("Signers: %v\n", localVault.Signers)
	fmt.Printf("KeyShares: %d\n", len(localVault.KeyShares))
	fmt.Printf("LibType: %d (0=GG20, 1=DKLS)\n", localVault.LibType)
	fmt.Printf("Saved to: %s\n", VaultStoragePath())

	// Check Fast Vault and authenticate
	isFastVault, err := CheckFastVaultExists(localVault.PublicKeyECDSA)
	if err != nil {
		fmt.Printf("\nWarning: Could not check Fast Vault Server: %v\n", err)
		return nil
	}

	if !isFastVault {
		fmt.Println("\nWarning: NOT a Fast Vault!")
		fmt.Println("  This vault was not found on the Fast Vault Server.")
		fmt.Println("  Plugin reshare operations will NOT work without Fast Vault.")
		fmt.Println("  Please use a vault created with the Vultisig app's Fast Vault feature.")
		return nil
	}

	fmt.Println("\nFast Vault: Yes (vault exists on Fast Vault Server)")

	// Auto-authenticate with verifier
	if password == "" {
		fmt.Println("\nTo authenticate, re-run with --password to provide Fast Vault password")
		return nil
	}

	fmt.Println("\nAuthenticating with verifier...")
	authStart := time.Now()
	err = authenticateVault(&localVault, password)
	authDuration := time.Since(authStart)

	if err != nil {
		fmt.Printf("\nWarning: Authentication failed: %v\n", err)
		fmt.Println("You can manually authenticate later with: devctl auth login --password xxx")
		return nil
	}

	totalDuration := time.Since(startTime)

	// Load the saved auth token for the report
	authToken, _ := LoadAuthToken()

	// Print completion report
	fmt.Println()
	fmt.Println("┌─────────────────────────────────────────────────────────────────┐")
	fmt.Println("│ IMPORT COMPLETE                                                 │")
	fmt.Println("├─────────────────────────────────────────────────────────────────┤")
	fmt.Println("│                                                                 │")
	fmt.Println("│  Source File:                                                   │")
	fmt.Printf("│    Path:    %-52s │\n", truncateStr(file, 52))
	fmt.Printf("│    Format:  %-52s │\n", format)
	fmt.Printf("│    Size:    %-52s │\n", formatFileSize(fileSize))
	fmt.Println("│                                                                 │")
	fmt.Println("│  Vault Saved:                                                   │")
	fmt.Printf("│    Location: %-51s │\n", truncateStr(VaultStoragePath(), 51))
	fmt.Printf("│    Name:     %-51s │\n", truncateStr(localVault.Name, 51))
	fmt.Printf("│    Parties:  %-51s │\n", fmt.Sprintf("%d signers", len(localVault.Signers)))
	fmt.Println("│                                                                 │")
	fmt.Println("│  Authentication:                                                │")
	fmt.Printf("│    Status:   %-51s │\n", "✓ Authenticated")
	if authToken != nil {
		fmt.Printf("│    Expires:  %-51s │\n", authToken.ExpiresAt.Format("2006-01-02 15:04:05"))
	}
	fmt.Printf("│    Duration: %-51s │\n", authDuration.Round(time.Millisecond).String())
	fmt.Println("│                                                                 │")
	fmt.Printf("│  Total Time: %-51s │\n", totalDuration.Round(time.Millisecond).String())
	fmt.Println("│                                                                 │")
	fmt.Println("└─────────────────────────────────────────────────────────────────┘")
	fmt.Println()
	fmt.Println("Next: ./devctl plugin install <plugin-id> -p <password>")

	return nil
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func formatFileSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d bytes", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
}

func authenticateVault(vault *LocalVault, password string) error {
	cfg, err := LoadConfig()
	if err != nil {
		cfg = DefaultConfig()
	}

	// Generate nonce for auth message
	nonceBytes := make([]byte, 16)
	_, err = rand.Read(nonceBytes)
	if err != nil {
		return fmt.Errorf("generate nonce: %w", err)
	}
	nonce := hex.EncodeToString(nonceBytes)
	expiryTime := time.Now().Add(5 * time.Minute)

	// Message must be JSON format for verifier
	messageObj := map[string]string{
		"nonce":     nonce,
		"expiresAt": expiryTime.Format(time.RFC3339),
	}
	messageJSON, err := json.Marshal(messageObj)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	message := string(messageJSON)

	fmt.Printf("  Vault: %s\n", vault.Name)
	fmt.Printf("  Verifier: %s\n", cfg.Verifier)

	// Create Ethereum-prefixed message hash for signing
	ethPrefixedMessage := fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(message), message)
	messageHash := crypto.Keccak256([]byte(ethPrefixedMessage))
	hexMessage := hex.EncodeToString(messageHash)

	// Perform TSS keysign
	tss := NewTSSService(vault.LocalPartyID)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	fmt.Println("  Performing TSS keysign...")

	derivePath := "m/44'/60'/0'/0/0"
	results, err := tss.KeysignWithFastVault(ctx, vault, []string{hexMessage}, derivePath, password)
	if err != nil {
		return fmt.Errorf("TSS keysign failed: %w", err)
	}

	if len(results) == 0 {
		return fmt.Errorf("no signature result")
	}

	// Build signature in Ethereum format (R + S + V)
	signature := "0x" + results[0].R + results[0].S + results[0].RecoveryID

	// Send auth request to verifier
	authReq := map[string]string{
		"message":        message,
		"signature":      signature,
		"chain_code_hex": vault.HexChainCode,
		"public_key":     vault.PublicKeyECDSA,
	}

	reqJSON, err := json.Marshal(authReq)
	if err != nil {
		return fmt.Errorf("marshal auth request: %w", err)
	}

	url := cfg.Verifier + "/auth"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqJSON))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("auth request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("authentication failed (%d): %s", resp.StatusCode, string(body))
	}

	var authResp struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	err = json.Unmarshal(body, &authResp)
	if err != nil {
		return fmt.Errorf("parse auth response: %w", err)
	}

	// Save token
	authToken := AuthToken{
		Token:     authResp.Data.Token,
		PublicKey: vault.PublicKeyECDSA,
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
	}

	err = SaveAuthToken(&authToken)
	if err != nil {
		return fmt.Errorf("save auth token: %w", err)
	}

	fmt.Printf("  Token expires: %s\n", authToken.ExpiresAt.Format(time.RFC3339))

	return nil
}

func CheckFastVaultExists(publicKey string) (bool, error) {
	url := fmt.Sprintf("%s/vault/exist/%s", FastVaultServer, publicKey)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK, nil
}

func parseVultFile(data []byte, password string) (*v1.Vault, error) {
	// Base64 decode the file content
	decoded, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}

	// Parse VaultContainer protobuf
	var container v1.VaultContainer
	err = proto.Unmarshal(decoded, &container)
	if err != nil {
		return nil, fmt.Errorf("unmarshal vault container: %w", err)
	}

	// Get vault bytes
	vaultBytes, err := base64.StdEncoding.DecodeString(container.Vault)
	if err != nil {
		return nil, fmt.Errorf("decode vault data: %w", err)
	}

	// Decrypt if encrypted
	if container.IsEncrypted {
		if password == "" {
			return nil, fmt.Errorf("vault is encrypted - password required (use --password)")
		}
		vaultBytes, err = common.DecryptVault(password, vaultBytes)
		if err != nil {
			return nil, fmt.Errorf("decrypt vault: %w", err)
		}
	}

	// Parse Vault protobuf
	var pbVault v1.Vault
	err = proto.Unmarshal(vaultBytes, &pbVault)
	if err != nil {
		return nil, fmt.Errorf("unmarshal vault: %w", err)
	}

	return &pbVault, nil
}

func convertProtoVaultToLocal(pbVault *v1.Vault) LocalVault {
	keyShares := make([]KeyShare, 0, len(pbVault.KeyShares))
	for _, ks := range pbVault.KeyShares {
		keyShares = append(keyShares, KeyShare{
			PubKey:   ks.PublicKey,
			Keyshare: ks.Keyshare,
		})
	}

	createdAt := ""
	if pbVault.CreatedAt != nil {
		createdAt = pbVault.CreatedAt.AsTime().Format(time.RFC3339)
	}

	return LocalVault{
		Name:           pbVault.Name,
		PublicKeyECDSA: pbVault.PublicKeyEcdsa,
		PublicKeyEdDSA: pbVault.PublicKeyEddsa,
		HexChainCode:   pbVault.HexChainCode,
		LocalPartyID:   pbVault.LocalPartyId,
		Signers:        pbVault.Signers,
		KeyShares:      keyShares,
		ResharePrefix:  pbVault.ResharePrefix,
		CreatedAt:      createdAt,
		LibType:        int(pbVault.LibType),
	}
}

func runVaultExport(output string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if cfg.PublicKeyECDSA == "" {
		return fmt.Errorf("no vault configured")
	}

	vault, err := LoadVault(cfg.PublicKeyECDSA[:16])
	if err != nil {
		return fmt.Errorf("load vault: %w", err)
	}

	data, err := json.MarshalIndent(vault, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal vault: %w", err)
	}

	if output == "" {
		output = fmt.Sprintf("%s-vault.json", vault.Name)
	}

	err = os.WriteFile(output, data, 0600)
	if err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	fmt.Printf("Vault exported to: %s\n", output)

	return nil
}

func runVaultUse(pubKeyPrefix string) error {
	vault, err := LoadVault(pubKeyPrefix)
	if err != nil {
		return fmt.Errorf("vault not found: %w", err)
	}

	cfg, _ := LoadConfig()
	cfg.VaultName = vault.Name
	cfg.PublicKeyECDSA = vault.PublicKeyECDSA
	cfg.PublicKeyEdDSA = vault.PublicKeyEdDSA
	err = SaveConfig(cfg)
	if err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Printf("Now using vault: %s\n", vault.Name)
	fmt.Printf("Public Key: %s...\n", vault.PublicKeyECDSA[:32])

	return nil
}

func newVaultBalanceCmd() *cobra.Command {
	var chain string

	cmd := &cobra.Command{
		Use:   "balance",
		Short: "Show vault balances on chains",
		Long: `Show the native token balance for the vault on various chains.

By default shows balances on all supported EVM chains.
Use --chain to filter to a specific chain.

Example:
  devctl vault balance
  devctl vault balance --chain ethereum
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultBalance(chain)
		},
	}

	cmd.Flags().StringVarP(&chain, "chain", "c", "", "Specific chain to check (ethereum, arbitrum, base, etc.)")

	return cmd
}

func newVaultAddressCmd() *cobra.Command {
	var chain string

	cmd := &cobra.Command{
		Use:   "address",
		Short: "Show vault addresses on chains",
		Long: `Show the derived addresses for the vault on various chains.

By default shows addresses for all supported chains.
Use --chain to filter to a specific chain.

Example:
  devctl vault address
  devctl vault address --chain ethereum
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultAddress(chain)
		},
	}

	cmd.Flags().StringVarP(&chain, "chain", "c", "", "Specific chain to show address for")

	return cmd
}

type ChainInfo struct {
	Name     string
	Chain    common.Chain
	RPCURL   string
	Symbol   string
	Decimals int
}

var supportedChains = []ChainInfo{
	{Name: "Ethereum", Chain: common.Ethereum, RPCURL: "https://ethereum-rpc.publicnode.com", Symbol: "ETH", Decimals: 18},
	{Name: "Arbitrum", Chain: common.Arbitrum, RPCURL: "https://arbitrum-one-rpc.publicnode.com", Symbol: "ETH", Decimals: 18},
	{Name: "Base", Chain: common.Base, RPCURL: "https://base-rpc.publicnode.com", Symbol: "ETH", Decimals: 18},
	{Name: "Polygon", Chain: common.Polygon, RPCURL: "https://polygon-bor-rpc.publicnode.com", Symbol: "MATIC", Decimals: 18},
	{Name: "BSC", Chain: common.BscChain, RPCURL: "https://bsc-rpc.publicnode.com", Symbol: "BNB", Decimals: 18},
	{Name: "Avalanche", Chain: common.Avalanche, RPCURL: "https://avalanche-c-chain-rpc.publicnode.com", Symbol: "AVAX", Decimals: 18},
	{Name: "Optimism", Chain: common.Optimism, RPCURL: "https://optimism-rpc.publicnode.com", Symbol: "ETH", Decimals: 18},
}

func runVaultAddress(chainFilter string) error {
	vaults, err := ListVaults()
	if err != nil || len(vaults) == 0 {
		return fmt.Errorf("no vaults found. Import a vault first: devctl vault import")
	}
	vault := vaults[0]

	fmt.Printf("=== Vault Addresses ===\n")
	fmt.Printf("Vault: %s\n\n", vault.Name)

	for _, c := range supportedChains {
		if chainFilter != "" && !strings.EqualFold(c.Name, chainFilter) && !strings.EqualFold(string(c.Chain), chainFilter) {
			continue
		}

		addr, _, _, err := address.GetAddress(vault.PublicKeyECDSA, vault.HexChainCode, c.Chain)
		if err != nil {
			fmt.Printf("  %s: error deriving address\n", c.Name)
			continue
		}

		fmt.Printf("  %s: %s\n", c.Name, addr)
	}

	if vault.PublicKeyEdDSA != "" {
		fmt.Println("\nEdDSA Chains:")
		solAddr, _, _, err := address.GetAddress(vault.PublicKeyEdDSA, vault.HexChainCode, common.Solana)
		if err == nil {
			fmt.Printf("  Solana: %s\n", solAddr)
		}
	}

	return nil
}

func runVaultBalance(chainFilter string) error {
	vaults, err := ListVaults()
	if err != nil || len(vaults) == 0 {
		return fmt.Errorf("no vaults found. Import a vault first: devctl vault import")
	}
	vault := vaults[0]

	fmt.Printf("=== Vault Balances ===\n")
	fmt.Printf("Vault: %s\n\n", vault.Name)

	for _, c := range supportedChains {
		if chainFilter != "" && !strings.EqualFold(c.Name, chainFilter) && !strings.EqualFold(string(c.Chain), chainFilter) {
			continue
		}

		addr, _, _, err := address.GetAddress(vault.PublicKeyECDSA, vault.HexChainCode, c.Chain)
		if err != nil {
			fmt.Printf("  %s: error deriving address\n", c.Name)
			continue
		}

		balance, err := getEVMBalance(c.RPCURL, addr)
		if err != nil {
			fmt.Printf("  %s: error fetching balance\n", c.Name)
			continue
		}

		balanceFloat := new(big.Float).Quo(
			new(big.Float).SetInt(balance),
			new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(c.Decimals)), nil)),
		)

		fmt.Printf("  %s: %s %s (%s)\n", c.Name, balanceFloat.Text('f', 6), c.Symbol, addr[:10]+"...")
	}

	return nil
}

func getEVMBalance(rpcURL, address string) (*big.Int, error) {
	payload := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "eth_getBalance",
		"params":  []interface{}{address, "latest"},
		"id":      1,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", rpcURL, strings.NewReader(string(payloadBytes)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Result string `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	err = json.Unmarshal(body, &result)
	if err != nil {
		return nil, err
	}

	if result.Error != nil {
		return nil, fmt.Errorf("RPC error: %s", result.Error.Message)
	}

	balanceHex := strings.TrimPrefix(result.Result, "0x")
	balance := new(big.Int)
	balance.SetString(balanceHex, 16)

	return balance, nil
}

var _ = hex.EncodeToString

// Token info for ERC20 balance checks
type TokenInfo struct {
	Symbol   string
	Address  string
	Decimals int
}

// Common tokens on Ethereum mainnet
var ethereumTokens = []TokenInfo{
	{Symbol: "USDT", Address: "0xdAC17F958D2ee523a2206206994597C13D831ec7", Decimals: 6},
	{Symbol: "USDC", Address: "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", Decimals: 6},
	{Symbol: "DAI", Address: "0x6B175474E89094C44Da98b954EesD5C4BB76F7Ed", Decimals: 18},
	{Symbol: "WETH", Address: "0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2", Decimals: 18},
}

func newVaultDetailsCmd() *cobra.Command {
	var chain string

	cmd := &cobra.Command{
		Use:   "details",
		Short: "Show detailed vault info with addresses and token balances",
		Long: `Show comprehensive vault details including:
- All chain addresses
- Native token balances
- Common ERC20 token balances (USDT, USDC, etc.)

This is useful for preparing DCA policies.

Example:
  devctl vault details
  devctl vault details --chain ethereum
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultDetails(chain)
		},
	}

	cmd.Flags().StringVarP(&chain, "chain", "c", "", "Specific chain to check (ethereum, arbitrum, base, etc.)")

	return cmd
}

func runVaultDetails(chainFilter string) error {
	vaults, err := ListVaults()
	if err != nil || len(vaults) == 0 {
		return fmt.Errorf("no vaults found. Import a vault first: devctl vault import")
	}
	vault := vaults[0]

	fmt.Println("╔══════════════════════════════════════════════════════════════════╗")
	fmt.Println("║                      VAULT DETAILS                               ║")
	fmt.Println("╚══════════════════════════════════════════════════════════════════╝")
	fmt.Printf("  Name:           %s\n", vault.Name)
	fmt.Printf("  ECDSA Key:      %s...\n", vault.PublicKeyECDSA[:20])
	if vault.PublicKeyEdDSA != "" {
		fmt.Printf("  EdDSA Key:      %s...\n", vault.PublicKeyEdDSA[:20])
	}
	fmt.Println()

	// Get EVM address (same for all EVM chains)
	evmAddr, _, _, err := address.GetAddress(vault.PublicKeyECDSA, vault.HexChainCode, common.Ethereum)
	if err != nil {
		return fmt.Errorf("derive EVM address: %w", err)
	}

	// EVM Chains section - consolidated
	if chainFilter == "" || isEVMChain(chainFilter) {
		fmt.Printf("┌─────────────────────────────────────────────────────────────────┐\n")
		fmt.Printf("│ EVM CHAINS                                                      │\n")
		fmt.Printf("├─────────────────────────────────────────────────────────────────┤\n")
		fmt.Printf("│ Address: %s\n", evmAddr)
		fmt.Printf("│\n")

		for _, c := range supportedChains {
			if chainFilter != "" && !strings.EqualFold(c.Name, chainFilter) && !strings.EqualFold(string(c.Chain), chainFilter) {
				continue
			}

			balance, err := getEVMBalance(c.RPCURL, evmAddr)
			if err != nil {
				fmt.Printf("│ %-12s %s: error\n", c.Name+":", c.Symbol)
			} else {
				balanceFloat := formatBalance(balance, c.Decimals)
				fmt.Printf("│ %-12s %s: %s\n", c.Name+":", c.Symbol, balanceFloat)
			}

			// Token balances for Ethereum mainnet
			if c.Chain == common.Ethereum {
				for _, token := range ethereumTokens {
					tokenBalance, err := getERC20Balance(c.RPCURL, token.Address, evmAddr)
					if err != nil {
						continue
					}
					if tokenBalance.Cmp(big.NewInt(0)) > 0 {
						balanceFloat := formatBalance(tokenBalance, token.Decimals)
						fmt.Printf("│ %-12s %s: %s\n", "", token.Symbol, balanceFloat)
					}
				}
			}
		}

		fmt.Printf("└─────────────────────────────────────────────────────────────────┘\n")
		fmt.Println()
	}

	// Bitcoin
	if chainFilter == "" || strings.EqualFold(chainFilter, "bitcoin") || strings.EqualFold(chainFilter, "btc") {
		btcAddr, _, _, err := address.GetAddress(vault.PublicKeyECDSA, vault.HexChainCode, common.Bitcoin)
		if err == nil {
			fmt.Printf("┌─────────────────────────────────────────────────────────────────┐\n")
			fmt.Printf("│ Bitcoin                                                         │\n")
			fmt.Printf("├─────────────────────────────────────────────────────────────────┤\n")
			fmt.Printf("│ Address: %s\n", btcAddr)
			fmt.Printf("│ BTC: (use explorer to check balance)\n")
			fmt.Printf("└─────────────────────────────────────────────────────────────────┘\n")
			fmt.Println()
		}
	}

	// THORChain
	if chainFilter == "" || strings.EqualFold(chainFilter, "thorchain") || strings.EqualFold(chainFilter, "rune") {
		thorAddr, _, _, err := address.GetAddress(vault.PublicKeyECDSA, vault.HexChainCode, common.THORChain)
		if err == nil {
			fmt.Printf("┌─────────────────────────────────────────────────────────────────┐\n")
			fmt.Printf("│ THORChain                                                       │\n")
			fmt.Printf("├─────────────────────────────────────────────────────────────────┤\n")
			fmt.Printf("│ Address: %s\n", thorAddr)
			fmt.Printf("│ RUNE: (use explorer to check balance)\n")
			fmt.Printf("└─────────────────────────────────────────────────────────────────┘\n")
			fmt.Println()
		}
	}

	// Maya
	if chainFilter == "" || strings.EqualFold(chainFilter, "maya") || strings.EqualFold(chainFilter, "cacao") {
		mayaAddr, _, _, err := address.GetAddress(vault.PublicKeyECDSA, vault.HexChainCode, common.MayaChain)
		if err == nil {
			fmt.Printf("┌─────────────────────────────────────────────────────────────────┐\n")
			fmt.Printf("│ MayaChain                                                       │\n")
			fmt.Printf("├─────────────────────────────────────────────────────────────────┤\n")
			fmt.Printf("│ Address: %s\n", mayaAddr)
			fmt.Printf("│ CACAO: (use explorer to check balance)\n")
			fmt.Printf("└─────────────────────────────────────────────────────────────────┘\n")
			fmt.Println()
		}
	}

	// Cosmos chains
	cosmosChains := []struct {
		name   string
		chain  common.Chain
		symbol string
	}{
		{"Cosmos Hub", common.GaiaChain, "ATOM"},
		{"Osmosis", common.Osmosis, "OSMO"},
		{"Dydx", common.Dydx, "DYDX"},
		{"Kujira", common.Kujira, "KUJI"},
	}

	for _, cc := range cosmosChains {
		if chainFilter == "" || strings.EqualFold(chainFilter, cc.name) || strings.EqualFold(chainFilter, cc.symbol) {
			addr, _, _, err := address.GetAddress(vault.PublicKeyECDSA, vault.HexChainCode, cc.chain)
			if err == nil {
				fmt.Printf("┌─────────────────────────────────────────────────────────────────┐\n")
				fmt.Printf("│ %s\n", cc.name)
				fmt.Printf("├─────────────────────────────────────────────────────────────────┤\n")
				fmt.Printf("│ Address: %s\n", addr)
				fmt.Printf("│ %s: (use explorer to check balance)\n", cc.symbol)
				fmt.Printf("└─────────────────────────────────────────────────────────────────┘\n")
				fmt.Println()
			}
		}
	}

	// Solana (EdDSA)
	if vault.PublicKeyEdDSA != "" {
		if chainFilter == "" || strings.EqualFold(chainFilter, "solana") || strings.EqualFold(chainFilter, "sol") {
			solAddr, _, _, err := address.GetAddress(vault.PublicKeyEdDSA, vault.HexChainCode, common.Solana)
			if err == nil {
				fmt.Printf("┌─────────────────────────────────────────────────────────────────┐\n")
				fmt.Printf("│ Solana (EdDSA)                                                  │\n")
				fmt.Printf("├─────────────────────────────────────────────────────────────────┤\n")
				fmt.Printf("│ Address: %s\n", solAddr)
				fmt.Printf("│ SOL: (use explorer to check balance)\n")
				fmt.Printf("└─────────────────────────────────────────────────────────────────┘\n")
				fmt.Println()
			}
		}

		// Sui
		if chainFilter == "" || strings.EqualFold(chainFilter, "sui") {
			suiAddr, _, _, err := address.GetAddress(vault.PublicKeyEdDSA, vault.HexChainCode, common.Sui)
			if err == nil {
				fmt.Printf("┌─────────────────────────────────────────────────────────────────┐\n")
				fmt.Printf("│ Sui (EdDSA)                                                     │\n")
				fmt.Printf("├─────────────────────────────────────────────────────────────────┤\n")
				fmt.Printf("│ Address: %s\n", suiAddr)
				fmt.Printf("│ SUI: (use explorer to check balance)\n")
				fmt.Printf("└─────────────────────────────────────────────────────────────────┘\n")
				fmt.Println()
			}
		}

		// Polkadot
		if chainFilter == "" || strings.EqualFold(chainFilter, "polkadot") || strings.EqualFold(chainFilter, "dot") {
			dotAddr, _, _, err := address.GetAddress(vault.PublicKeyEdDSA, vault.HexChainCode, common.Polkadot)
			if err == nil {
				fmt.Printf("┌─────────────────────────────────────────────────────────────────┐\n")
				fmt.Printf("│ Polkadot (EdDSA)                                                │\n")
				fmt.Printf("├─────────────────────────────────────────────────────────────────┤\n")
				fmt.Printf("│ Address: %s\n", dotAddr)
				fmt.Printf("│ DOT: (use explorer to check balance)\n")
				fmt.Printf("└─────────────────────────────────────────────────────────────────┘\n")
				fmt.Println()
			}
		}

		// Ton
		if chainFilter == "" || strings.EqualFold(chainFilter, "ton") {
			tonAddr, _, _, err := address.GetAddress(vault.PublicKeyEdDSA, vault.HexChainCode, common.Ton)
			if err == nil {
				fmt.Printf("┌─────────────────────────────────────────────────────────────────┐\n")
				fmt.Printf("│ TON (EdDSA)                                                     │\n")
				fmt.Printf("├─────────────────────────────────────────────────────────────────┤\n")
				fmt.Printf("│ Address: %s\n", tonAddr)
				fmt.Printf("│ TON: (use explorer to check balance)\n")
				fmt.Printf("└─────────────────────────────────────────────────────────────────┘\n")
				fmt.Println()
			}
		}
	}

	return nil
}

func isEVMChain(chainFilter string) bool {
	evmNames := []string{"ethereum", "eth", "arbitrum", "arb", "base", "polygon", "matic", "bsc", "bnb", "avalanche", "avax", "optimism", "op"}
	filterLower := strings.ToLower(chainFilter)
	for _, name := range evmNames {
		if filterLower == name {
			return true
		}
	}
	return false
}

func formatBalance(balance *big.Int, decimals int) string {
	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	balanceFloat := new(big.Float).Quo(
		new(big.Float).SetInt(balance),
		new(big.Float).SetInt(divisor),
	)
	return balanceFloat.Text('f', 6)
}

func getERC20Balance(rpcURL, tokenAddress, walletAddress string) (*big.Int, error) {
	// balanceOf(address) selector = 0x70a08231
	// Pad address to 32 bytes
	paddedAddress := fmt.Sprintf("000000000000000000000000%s", strings.TrimPrefix(walletAddress, "0x"))
	data := "0x70a08231" + paddedAddress

	payload := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "eth_call",
		"params": []interface{}{
			map[string]string{
				"to":   tokenAddress,
				"data": data,
			},
			"latest",
		},
		"id": 1,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", rpcURL, strings.NewReader(string(payloadBytes)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Result string `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	err = json.Unmarshal(body, &result)
	if err != nil {
		return nil, err
	}

	if result.Error != nil {
		return nil, fmt.Errorf("RPC error: %s", result.Error.Message)
	}

	if result.Result == "" || result.Result == "0x" {
		return big.NewInt(0), nil
	}

	balanceHex := strings.TrimPrefix(result.Result, "0x")
	balance := new(big.Int)
	balance.SetString(balanceHex, 16)

	return balance, nil
}
