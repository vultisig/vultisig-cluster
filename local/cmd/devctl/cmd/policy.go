package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	rtypes "github.com/vultisig/recipes/types"
	"github.com/vultisig/vultisig-go/address"
	"github.com/vultisig/vultisig-go/common"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func NewPolicyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Policy management commands",
	}

	cmd.AddCommand(newPolicyListCmd())
	cmd.AddCommand(newPolicyCreateCmd())
	cmd.AddCommand(newPolicyDeleteCmd())
	cmd.AddCommand(newPolicyInfoCmd())
	cmd.AddCommand(newPolicyHistoryCmd())
	cmd.AddCommand(newPolicyStatusCmd())
	cmd.AddCommand(newPolicyTransactionsCmd())
	cmd.AddCommand(newPolicyTriggerCmd())

	return cmd
}

func newPolicyListCmd() *cobra.Command {
	var pluginID string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List policies for a plugin",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPolicyList(pluginID)
		},
	}

	cmd.Flags().StringVarP(&pluginID, "plugin", "p", "", "Plugin ID (required)")
	cmd.MarkFlagRequired("plugin")

	return cmd
}

func newPolicyCreateCmd() *cobra.Command {
	var pluginID string
	var configFile string
	var password string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new policy",
		Long: `Create a new policy for a plugin.

The policy configuration should be a JSON file containing:
{
  "recipe": {
    // Recipe-specific configuration (varies by plugin)
  },
  "billing": [
    { "type": "once", "amount": 0 }
  ]
}

Example for DCA plugin (swap ETH to USDC):
{
  "recipe": {
    "from": { "chain": "Ethereum", "token": "", "address": "" },
    "to": { "chain": "Ethereum", "token": "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48", "address": "" },
    "fromAmount": "1000000000000000",
    "frequency": "daily"
  },
  "billing": [{ "type": "once", "amount": 0 }]
}

Environment variables:
  VAULT_PASSWORD  - Fast Vault password

Note: Requires authentication. Run 'devctl vault import' first.
`,
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
			return runPolicyCreate(pluginID, configFile, actualPassword)
		},
	}

	cmd.Flags().StringVarP(&pluginID, "plugin", "p", "", "Plugin ID (required)")
	cmd.Flags().StringVarP(&configFile, "config", "c", "", "Policy configuration file (required)")
	cmd.Flags().StringVar(&password, "password", "", "Fast Vault password (or set VAULT_PASSWORD env var)")
	cmd.MarkFlagRequired("plugin")
	cmd.MarkFlagRequired("config")

	return cmd
}

func newPolicyDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete [policy-id]",
		Short: "Delete a policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPolicyDelete(args[0])
		},
	}
}

func newPolicyInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info [policy-id]",
		Short: "Show policy details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPolicyInfo(args[0])
		},
	}
}

func newPolicyHistoryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "history [policy-id]",
		Short: "Show policy transaction history",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPolicyHistory(args[0])
		},
	}
}

func runPolicyList(pluginID string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	authHeader, err := GetAuthHeader()
	if err != nil {
		return fmt.Errorf("authentication required: %w\n\nRun 'devctl vault import' first", err)
	}

	vaults, err := ListVaults()
	if err != nil || len(vaults) == 0 {
		return fmt.Errorf("no vaults found. Import a vault first: devctl vault import")
	}
	publicKey := vaults[0].PublicKeyECDSA

	fmt.Printf("Fetching policies for plugin %s...\n", pluginID)
	fmt.Printf("  Vault: %s...\n\n", publicKey[:20])

	url := fmt.Sprintf("%s/plugin/policies/%s?public_key=%s", cfg.Verifier, pluginID, publicKey)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", authHeader)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("request failed (%d): %s", resp.StatusCode, string(body))
	}

	var policies []map[string]interface{}
	err = json.Unmarshal(body, &policies)
	if err != nil {
		var result map[string]interface{}
		json.Unmarshal(body, &result)
		prettyJSON, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(prettyJSON))
		return nil
	}

	if len(policies) == 0 {
		fmt.Println("No policies found for this plugin.")
		return nil
	}

	fmt.Printf("Found %d policies:\n\n", len(policies))
	for i, p := range policies {
		policyID := p["id"]
		active := p["active"]
		createdAt := p["created_at"]
		fmt.Printf("  %d. Policy ID: %v\n", i+1, policyID)
		fmt.Printf("     Active: %v\n", active)
		fmt.Printf("     Created: %v\n\n", createdAt)
	}

	return nil
}

func runPolicyCreate(pluginID, configFile string, password string) error {
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

	configData, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("read config file: %w", err)
	}

	var policyConfig map[string]interface{}
	err = json.Unmarshal(configData, &policyConfig)
	if err != nil {
		return fmt.Errorf("parse config file: %w", err)
	}

	recipeConfig, ok := policyConfig["recipe"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("missing or invalid 'recipe' in config file")
	}

	// Auto-fill addresses from vault if empty
	recipeConfig, err = fillAddressesFromVault(recipeConfig, vault)
	if err != nil {
		return fmt.Errorf("fill addresses from vault: %w", err)
	}

	fmt.Printf("Creating policy for plugin %s...\n", pluginID)
	fmt.Printf("  Vault: %s (%s...)\n", vault.Name, vault.PublicKeyECDSA[:16])
	fmt.Printf("  Config: %s\n", configFile)

	// Step 1: Get plugin server URL
	pluginServerURL, err := getPluginServerURL(cfg.Verifier, pluginID)
	if err != nil {
		return fmt.Errorf("get plugin server URL: %w", err)
	}
	fmt.Printf("  Plugin Server: %s\n", pluginServerURL)

	// Step 2: Call plugin's suggest endpoint to get rules
	fmt.Println("\nFetching policy template from plugin...")
	policySuggest, err := getPluginPolicySuggest(pluginServerURL, recipeConfig)
	if err != nil {
		return fmt.Errorf("get policy suggest: %w", err)
	}
	fmt.Printf("  Rules: %d\n", len(policySuggest.GetRules()))
	if policySuggest.RateLimitWindow != nil {
		fmt.Printf("  Rate Limit Window: %ds\n", policySuggest.GetRateLimitWindow())
	}

	// Step 3: Build protobuf Policy
	policy, err := buildProtobufPolicy(pluginID, recipeConfig, policyConfig["billing"], policySuggest)
	if err != nil {
		return fmt.Errorf("build protobuf policy: %w", err)
	}

	// Step 4: Serialize to protobuf bytes, then base64
	policyBytes, err := proto.Marshal(policy)
	if err != nil {
		return fmt.Errorf("marshal protobuf policy: %w", err)
	}
	recipeBase64 := base64.StdEncoding.EncodeToString(policyBytes)

	policyVersion := 1
	pluginVersion := "1.0.0"

	// Step 5: Create signature message and sign
	// Message format: {recipe}*#*{public_key}*#*{policy_version}*#*{plugin_version}
	signatureMessage := fmt.Sprintf("%s*#*%s*#*%d*#*%s",
		recipeBase64,
		vault.PublicKeyECDSA,
		policyVersion,
		pluginVersion,
	)

	// DEBUG: print message details
	fmt.Printf("\n  DEBUG: Signing message:\n")
	fmt.Printf("    Recipe (first 50 chars): %s...\n", recipeBase64[:min(50, len(recipeBase64))])
	fmt.Printf("    Public Key: %s\n", vault.PublicKeyECDSA)
	fmt.Printf("    Policy Version: %d\n", policyVersion)
	fmt.Printf("    Plugin Version: %s\n", pluginVersion)
	fmt.Printf("    Full message length: %d\n", len(signatureMessage))

	ethPrefixedMessage := fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(signatureMessage), signatureMessage)
	messageHash := crypto.Keccak256([]byte(ethPrefixedMessage))
	hexMessage := hex.EncodeToString(messageHash)
	fmt.Printf("    Message hash: %s\n", hexMessage)

	fmt.Println("\nSigning policy with TSS keysign (2-of-2 with Fast Vault Server)...")

	if password == "" {
		return fmt.Errorf("password is required for TSS keysign. Use --password flag")
	}

	tss := NewTSSService(vault.LocalPartyID)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	derivePath := "m/44'/60'/0'/0/0"
	results, err := tss.KeysignWithFastVault(ctx, vault, []string{hexMessage}, derivePath, password)
	if err != nil {
		return fmt.Errorf("TSS keysign failed: %w", err)
	}

	if len(results) == 0 {
		return fmt.Errorf("no signature result")
	}

	// Build signature in Ethereum format (R + S + V) - same as auth signing
	signature := "0x" + results[0].R + results[0].S + results[0].RecoveryID
	fmt.Printf("  DEBUG: Signature: %s\n", signature)
	fmt.Printf("  DEBUG: R: %s, S: %s, V: %s\n", results[0].R, results[0].S, results[0].RecoveryID)

	// Step 6: Build billing array for API request
	billingArray, err := buildBillingArray(policyConfig["billing"])
	if err != nil {
		return fmt.Errorf("build billing array: %w", err)
	}

	policyRequest := map[string]interface{}{
		"plugin_id":      pluginID,
		"public_key":     vault.PublicKeyECDSA,
		"plugin_version": pluginVersion,
		"policy_version": policyVersion,
		"signature":      signature,
		"recipe":         recipeBase64,
		"billing":        billingArray,
		"active":         true,
	}

	policyJSON, err := json.Marshal(policyRequest)
	if err != nil {
		return fmt.Errorf("marshal policy request: %w", err)
	}

	// Step 7: Submit to verifier
	fmt.Println("\nSubmitting policy to verifier...")

	url := cfg.Verifier + "/plugin/policy"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(policyJSON))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("submit policy: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("create policy failed (%d): %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	json.Unmarshal(body, &result)

	totalDuration := time.Since(startTime)

	// Print completion report
	fmt.Println()
	fmt.Println("┌─────────────────────────────────────────────────────────────────┐")
	fmt.Println("│ POLICY CREATED SUCCESSFULLY                                     │")
	fmt.Println("├─────────────────────────────────────────────────────────────────┤")
	fmt.Println("│                                                                 │")
	fmt.Printf("│  Plugin:      %-50s │\n", pluginID)
	fmt.Printf("│  Vault:       %-50s │\n", vault.PublicKeyECDSA[:16]+"...")
	if data, ok := result["data"].(map[string]interface{}); ok {
		if id, ok := data["id"].(string); ok {
			fmt.Printf("│  Policy ID:   %-50s │\n", id)
		}
	}
	fmt.Printf("│  Rules:       %-50d │\n", len(policySuggest.GetRules()))
	fmt.Println("│                                                                 │")
	fmt.Printf("│  Total Time:  %-50s │\n", totalDuration.Round(time.Millisecond).String())
	fmt.Println("│                                                                 │")
	fmt.Println("└─────────────────────────────────────────────────────────────────┘")

	return nil
}

func getPluginServerURL(verifierURL, pluginID string) (string, error) {
	// For local dev, use hardcoded URLs
	pluginURLs := map[string]string{
		"vultisig-dca-0000":             "http://localhost:8082",
		"vultisig-fees-feee":            "http://localhost:8085",
		"vultisig-recurring-sends-0000": "http://localhost:8083",
	}

	if url, ok := pluginURLs[pluginID]; ok {
		return url, nil
	}

	return "", fmt.Errorf("unknown plugin ID: %s", pluginID)
}

func getPluginPolicySuggest(pluginServerURL string, recipeConfig map[string]interface{}) (*rtypes.PolicySuggest, error) {
	reqBody, err := json.Marshal(map[string]interface{}{
		"configuration": recipeConfig,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", pluginServerURL+"/plugin/recipe-specification/suggest", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("suggest failed (%d): %s", resp.StatusCode, string(body))
	}

	policySuggest := &rtypes.PolicySuggest{}
	err = protojson.Unmarshal(body, policySuggest)
	if err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return policySuggest, nil
}

func buildProtobufPolicy(pluginID string, recipeConfig map[string]interface{}, billingConfig interface{}, suggest *rtypes.PolicySuggest) (*rtypes.Policy, error) {
	// Build Configuration from recipe config
	configuration, err := structpb.NewStruct(recipeConfig)
	if err != nil {
		return nil, fmt.Errorf("build configuration struct: %w", err)
	}

	// Build FeePolicies from billing config
	feePolicies, err := buildFeePolicies(billingConfig)
	if err != nil {
		return nil, fmt.Errorf("build fee policies: %w", err)
	}

	// Policy ID must match the plugin ID for schema validation
	policy := &rtypes.Policy{
		Id:            pluginID,
		Configuration: configuration,
		Rules:         suggest.GetRules(),
		FeePolicies:   feePolicies,
	}

	if suggest.RateLimitWindow != nil {
		policy.RateLimitWindow = suggest.RateLimitWindow
	}
	if suggest.MaxTxsPerWindow != nil {
		policy.MaxTxsPerWindow = suggest.MaxTxsPerWindow
	}

	return policy, nil
}

func buildFeePolicies(billingConfig interface{}) ([]*rtypes.FeePolicy, error) {
	if billingConfig == nil {
		return nil, nil
	}

	var billingArray []interface{}

	switch v := billingConfig.(type) {
	case []interface{}:
		billingArray = v
	case map[string]interface{}:
		billingArray = []interface{}{v}
	default:
		return nil, fmt.Errorf("invalid billing config type: %T", billingConfig)
	}

	var feePolicies []*rtypes.FeePolicy
	for _, item := range billingArray {
		billing, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		feePolicy := &rtypes.FeePolicy{
			Id: uuid.New().String(),
		}

		if typeStr, ok := billing["type"].(string); ok {
			switch strings.ToLower(typeStr) {
			case "once", "one_time", "one-time":
				feePolicy.Type = rtypes.FeeType_ONCE
			case "transaction", "per_tx", "per-tx":
				feePolicy.Type = rtypes.FeeType_TRANSACTION
			case "recurring":
				feePolicy.Type = rtypes.FeeType_RECURRING
			default:
				feePolicy.Type = rtypes.FeeType_ONCE
			}
		}

		if amount, ok := billing["amount"].(float64); ok {
			feePolicy.Amount = int64(amount)
		}

		if freq, ok := billing["frequency"].(string); ok {
			switch strings.ToLower(freq) {
			case "daily":
				feePolicy.Frequency = rtypes.BillingFrequency_DAILY
			case "weekly":
				feePolicy.Frequency = rtypes.BillingFrequency_WEEKLY
			case "biweekly", "bi-weekly":
				feePolicy.Frequency = rtypes.BillingFrequency_BIWEEKLY
			case "monthly":
				feePolicy.Frequency = rtypes.BillingFrequency_MONTHLY
			}
		}

		feePolicies = append(feePolicies, feePolicy)
	}

	return feePolicies, nil
}

func buildBillingArray(billingConfig interface{}) ([]map[string]interface{}, error) {
	if billingConfig == nil {
		return []map[string]interface{}{}, nil
	}

	var billingArray []interface{}

	switch v := billingConfig.(type) {
	case []interface{}:
		billingArray = v
	case map[string]interface{}:
		billingArray = []interface{}{v}
	default:
		return nil, fmt.Errorf("invalid billing config type: %T", billingConfig)
	}

	var result []map[string]interface{}
	for _, item := range billingArray {
		if billing, ok := item.(map[string]interface{}); ok {
			result = append(result, billing)
		}
	}

	return result, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func runPolicyDelete(policyID string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	fmt.Printf("Deleting policy %s...\n", policyID)

	url := fmt.Sprintf("%s/plugin/policy/%s", cfg.Verifier, policyID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("Response (%d): %s\n", resp.StatusCode, string(body))

	return nil
}

func runPolicyInfo(policyID string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	fmt.Printf("Fetching policy %s...\n\n", policyID)

	url := fmt.Sprintf("%s/plugin/policy/%s", cfg.Verifier, policyID)
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

func runPolicyHistory(policyID string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	fmt.Printf("Fetching transaction history for policy %s...\n\n", policyID)

	url := fmt.Sprintf("%s/plugin/policies/%s/history", cfg.Verifier, policyID)
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

func fillAddressesFromVault(recipeConfig map[string]interface{}, vault *LocalVault) (map[string]interface{}, error) {
	fromAsset, hasFrom := recipeConfig["from"].(map[string]interface{})
	toAsset, hasTo := recipeConfig["to"].(map[string]interface{})

	if !hasFrom && !hasTo {
		return recipeConfig, nil
	}

	deriveAddress := func(chainStr string) (string, error) {
		chain, err := common.FromString(chainStr)
		if err != nil {
			return "", fmt.Errorf("unknown chain: %s", chainStr)
		}

		pubKey := vault.PublicKeyECDSA
		if chain == common.Solana {
			pubKey = vault.PublicKeyEdDSA
		}

		addr, _, _, err := address.GetAddress(pubKey, vault.HexChainCode, chain)
		if err != nil {
			return "", fmt.Errorf("derive address for %s: %w", chainStr, err)
		}
		return addr, nil
	}

	if hasFrom {
		chainStr, _ := fromAsset["chain"].(string)
		existingAddr, _ := fromAsset["address"].(string)
		if existingAddr == "" && chainStr != "" {
			addr, err := deriveAddress(chainStr)
			if err != nil {
				return nil, err
			}
			fromAsset["address"] = addr
			fmt.Printf("  Auto-filled from.address: %s\n", addr)
		}
	}

	if hasTo {
		chainStr, _ := toAsset["chain"].(string)
		existingAddr, _ := toAsset["address"].(string)
		if existingAddr == "" && chainStr != "" {
			addr, err := deriveAddress(chainStr)
			if err != nil {
				return nil, err
			}
			toAsset["address"] = addr
			fmt.Printf("  Auto-filled to.address: %s\n", addr)
		}
	}

	return recipeConfig, nil
}

func newPolicyStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status [policy-id]",
		Short: "Show policy status including scheduler info",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPolicyStatus(args[0])
		},
	}
	return cmd
}

func newPolicyTransactionsCmd() *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "transactions [policy-id]",
		Short: "Show transactions for a policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPolicyTransactions(args[0], limit)
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 10, "Number of transactions to show")
	return cmd
}

func newPolicyTriggerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trigger [policy-id]",
		Short: "Manually trigger policy execution (set next_execution = NOW)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPolicyTrigger(args[0])
		},
	}
	return cmd
}

func runPolicyStatus(policyID string) error {
	fmt.Printf("Policy Status: %s\n", policyID)
	fmt.Println(strings.Repeat("=", 50))

	policyActive, policyCreated := checkPolicyInDB(policyID)
	fmt.Printf("\nPolicy Record:\n")
	if policyCreated != "" {
		fmt.Printf("  Active:  %v\n", policyActive)
		fmt.Printf("  Created: %s\n", policyCreated)
	} else {
		fmt.Printf("  ✗ Not found in database\n")
	}

	nextExec := checkScheduler(policyID)
	fmt.Printf("\nScheduler:\n")
	if nextExec != "" {
		fmt.Printf("  Next Execution: %s\n", nextExec)
	} else {
		fmt.Printf("  ✗ Not scheduled (policy may be inactive or one-time completed)\n")
	}

	fmt.Printf("\nRecent Transactions:\n")
	txs := getRecentTransactions(policyID, 3)
	if len(txs) == 0 {
		fmt.Printf("  No transactions found\n")
	} else {
		for _, tx := range txs {
			fmt.Printf("  • %s | %s | %s\n", tx.Status, tx.TxHash, tx.CreatedAt)
		}
	}

	return nil
}

func runPolicyTransactions(policyID string, limit int) error {
	fmt.Printf("Transactions for Policy: %s\n", policyID)
	fmt.Println(strings.Repeat("=", 60))

	txs := getRecentTransactions(policyID, limit)
	if len(txs) == 0 {
		fmt.Println("\nNo transactions found for this policy.")
		fmt.Println("\nPossible reasons:")
		fmt.Println("  - Policy hasn't executed yet (check scheduler)")
		fmt.Println("  - Policy is inactive")
		fmt.Println("  - Scheduler hasn't picked it up (polls every 30s)")
		return nil
	}

	fmt.Printf("\nFound %d transactions:\n\n", len(txs))
	for i, tx := range txs {
		fmt.Printf("%d. TX Hash: %s\n", i+1, tx.TxHash)
		fmt.Printf("   Status: %s | On-chain: %s\n", tx.Status, tx.OnChainStatus)
		fmt.Printf("   Created: %s\n", tx.CreatedAt)
		if tx.TxHash != "" && tx.TxHash != "<nil>" {
			fmt.Printf("   Explorer: https://etherscan.io/tx/%s\n", tx.TxHash)
		}
		fmt.Println()
	}

	return nil
}

func runPolicyTrigger(policyID string) error {
	fmt.Printf("Triggering policy: %s\n", policyID)

	cmd := exec.Command("docker", "exec", "vultisig-postgres",
		"psql", "-U", "vultisig", "-d", "vultisig-dca", "-c",
		fmt.Sprintf("UPDATE scheduler SET next_execution = NOW() WHERE policy_id = '%s'", policyID))

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to update scheduler: %w\nOutput: %s", err, string(output))
	}

	result := strings.TrimSpace(string(output))
	if strings.Contains(result, "UPDATE 0") {
		fmt.Println("⚠ Policy not found in scheduler table.")
		fmt.Println("  This might mean:")
		fmt.Println("  - Policy doesn't exist")
		fmt.Println("  - Policy is inactive (one-time completed)")
		fmt.Println("  - Policy hasn't been scheduled yet")
		return nil
	}

	fmt.Println("✓ Policy triggered! Scheduler will pick it up within 30 seconds.")
	fmt.Println("\nMonitor with:")
	fmt.Println("  devctl policy status " + policyID)
	fmt.Println("  devctl policy transactions " + policyID)

	return nil
}

type TxRecord struct {
	TxHash        string
	Status        string
	OnChainStatus string
	CreatedAt     string
}

func checkPolicyInDB(policyID string) (bool, string) {
	cmd := exec.Command("docker", "exec", "vultisig-postgres",
		"psql", "-U", "vultisig", "-d", "vultisig-verifier", "-t", "-c",
		fmt.Sprintf("SELECT active, created_at FROM plugin_policies WHERE id = '%s' LIMIT 1", policyID))

	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, ""
	}

	result := strings.TrimSpace(string(output))
	if result == "" {
		return false, ""
	}

	parts := strings.Split(result, "|")
	if len(parts) < 2 {
		return false, ""
	}

	active := strings.TrimSpace(parts[0]) == "t"
	created := strings.TrimSpace(parts[1])
	return active, created
}

func checkScheduler(policyID string) string {
	cmd := exec.Command("docker", "exec", "vultisig-postgres",
		"psql", "-U", "vultisig", "-d", "vultisig-dca", "-t", "-c",
		fmt.Sprintf("SELECT next_execution FROM scheduler WHERE policy_id = '%s' LIMIT 1", policyID))

	output, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(output))
}

func getRecentTransactions(policyID string, limit int) []TxRecord {
	cmd := exec.Command("docker", "exec", "vultisig-postgres",
		"psql", "-U", "vultisig", "-d", "vultisig-dca", "-t", "-c",
		fmt.Sprintf(`SELECT tx_hash, status, status_onchain, created_at
			FROM tx_indexer
			WHERE policy_id = '%s'
			ORDER BY created_at DESC
			LIMIT %d`, policyID, limit))

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil
	}

	var txs []TxRecord
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 4 {
			continue
		}
		txs = append(txs, TxRecord{
			TxHash:        strings.TrimSpace(parts[0]),
			Status:        strings.TrimSpace(parts[1]),
			OnChainStatus: strings.TrimSpace(parts[2]),
			CreatedAt:     strings.TrimSpace(parts[3]),
		})
	}

	return txs
}
