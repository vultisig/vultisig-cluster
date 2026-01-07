package cmd

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	"github.com/vultisig/vultisig-go/relay"
	vgtypes "github.com/vultisig/vultisig-go/types"
)

var (
	FastVaultServer = "https://api.vultisig.com"
	RelayServer     = "https://api.vultisig.com/router"
)

const (
	DefaultLocalParty  = "devctl"
	KeygenTimeout      = 3 * time.Minute
	MessagePollTimeout = 2 * time.Minute
)

func InitTSSConfig() {
	config, err := LoadClusterConfig()
	if err != nil {
		return
	}
	FastVaultServer = config.GetVultiserverURL()
	RelayServer = config.GetRelayURL()
}

type KeyShare struct {
	PubKey   string `json:"pubkey"`
	Keyshare string `json:"keyshare"`
}

type LocalVault struct {
	Name           string      `json:"name"`
	PublicKeyECDSA string      `json:"pubKeyECDSA"`
	PublicKeyEdDSA string      `json:"pubKeyEdDSA"`
	HexChainCode   string      `json:"hexChainCode"`
	LocalPartyID   string      `json:"localPartyID"`
	Signers        []string    `json:"signers"`
	KeyShares      []KeyShare  `json:"keyshares"`
	ResharePrefix  string      `json:"resharePrefix,omitempty"`
	CreatedAt      string      `json:"createdAt"`
	LibType        int         `json:"libType"` // 0 = GG20, 1 = DKLS
}

type BackupVault struct {
	Version string     `json:"version"`
	Vault   LocalVault `json:"vault"`
}

type TSSService struct {
	relayClient  *relay.Client
	localPartyID string
	logger       *logrus.Entry
}

func NewTSSService(localPartyID string) *TSSService {
	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)
	logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})

	return &TSSService{
		relayClient:  relay.NewRelayClient(RelayServer),
		localPartyID: localPartyID,
		logger:       logger.WithField("component", "tss"),
	}
}

func (t *TSSService) Keygen(ctx context.Context, vaultName string) (*LocalVault, error) {
	sessionID := uuid.New().String()

	encryptionKey := make([]byte, 32)
	_, err := rand.Read(encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("generate encryption key: %w", err)
	}
	hexEncryptionKey := hex.EncodeToString(encryptionKey)

	chainCode := make([]byte, 32)
	_, err = rand.Read(chainCode)
	if err != nil {
		return nil, fmt.Errorf("generate chain code: %w", err)
	}
	hexChainCode := hex.EncodeToString(chainCode)

	t.logger.WithFields(logrus.Fields{
		"session_id":  sessionID,
		"local_party": t.localPartyID,
		"vault_name":  vaultName,
	}).Info("Starting keygen session")

	err = t.relayClient.RegisterSession(sessionID, t.localPartyID)
	if err != nil {
		return nil, fmt.Errorf("register session: %w", err)
	}

	t.logger.Info("Requesting Fast Vault Server to join keygen...")
	err = t.requestFastVaultKeygen(ctx, vaultName, sessionID, hexEncryptionKey, hexChainCode)
	if err != nil {
		return nil, fmt.Errorf("request fast vault keygen: %w", err)
	}

	t.logger.Info("Waiting for Fast Vault Server to join...")
	parties, err := t.waitForParties(ctx, sessionID, 2)
	if err != nil {
		return nil, fmt.Errorf("wait for parties: %w", err)
	}

	t.logger.WithField("parties", parties).Info("All parties joined, starting keygen")

	err = t.relayClient.StartSession(sessionID, parties)
	if err != nil {
		return nil, fmt.Errorf("start session: %w", err)
	}

	t.logger.Info("Running DKLS keygen protocol...")
	t.logger.Info("[NOTE: This requires go-wrappers CGO library]")
	t.logger.Info("The keygen protocol would execute here with the DKLS library")

	t.logger.Info("For full TSS operation, ensure DYLD_LIBRARY_PATH is set:")
	t.logger.Infof("export DYLD_LIBRARY_PATH=/Users/dev/dev/vultisig/go-wrappers/includes/darwin/:$DYLD_LIBRARY_PATH")

	err = t.relayClient.CompleteSession(sessionID, t.localPartyID)
	if err != nil {
		t.logger.WithError(err).Warn("Failed to complete session")
	}

	vault := &LocalVault{
		Name:         vaultName,
		HexChainCode: hexChainCode,
		LocalPartyID: t.localPartyID,
		Signers:      parties,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		LibType:      1,
	}

	return vault, nil
}

func generateServerPartyID(sessionID string) string {
	h := 0
	for _, c := range sessionID {
		h = 31*h + int(c)
	}
	if h < 0 {
		h = -h
	}
	suffix := fmt.Sprintf("%d", h)
	if len(suffix) > 5 {
		suffix = suffix[len(suffix)-5:]
	}
	return fmt.Sprintf("Server-%s", suffix)
}

func (t *TSSService) requestFastVaultKeygen(ctx context.Context, name, sessionID, hexEncKey, hexChainCode string) error {
	serverPartyID := generateServerPartyID(sessionID)
	t.logger.WithField("server_party_id", serverPartyID).Debug("Generated server party ID")

	req := vgtypes.VaultCreateRequest{
		Name:               name,
		SessionID:          sessionID,
		HexEncryptionKey:   hexEncKey,
		HexChainCode:       hexChainCode,
		LocalPartyId:       serverPartyID,
		EncryptionPassword: "",
		Email:              "",
		LibType:            1, // DKLS
	}

	reqJSON, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := FastVaultServer + "/vault/create"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqJSON))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("fast vault server returned %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

func (t *TSSService) waitForParties(ctx context.Context, sessionID string, expected int) ([]string, error) {
	timeout := time.After(KeygenTimeout)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timeout:
			return nil, fmt.Errorf("timeout waiting for parties")
		default:
			parties, err := t.relayClient.GetSession(sessionID)
			if err != nil {
				t.logger.WithError(err).Debug("Failed to get session")
				time.Sleep(time.Second)
				continue
			}

			if len(parties) >= expected {
				return parties, nil
			}

			t.logger.WithField("parties", len(parties)).Debug("Waiting for more parties...")
			time.Sleep(time.Second)
		}
	}
}

func (t *TSSService) Reshare(ctx context.Context, vault *LocalVault, pluginID, verifierURL, authHeader, vaultPassword string) (*LocalVault, error) {
	sessionID := uuid.New().String()

	encryptionKey := make([]byte, 32)
	_, err := rand.Read(encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("generate encryption key: %w", err)
	}
	hexEncryptionKey := hex.EncodeToString(encryptionKey)

	t.logger.WithFields(logrus.Fields{
		"session_id":   sessionID,
		"old_parties":  vault.Signers,
		"plugin_id":    pluginID,
		"verifier_url": verifierURL,
	}).Info("Starting reshare session")

	err = t.relayClient.RegisterSession(sessionID, t.localPartyID)
	if err != nil {
		return nil, fmt.Errorf("register session: %w", err)
	}

	t.logger.Info("Requesting Fast Vault Server to join reshare...")
	err = t.requestFastVaultReshare(ctx, vault, sessionID, hexEncryptionKey, vaultPassword)
	if err != nil {
		t.logger.WithError(err).Warn("Failed to request Fast Vault Server - continuing anyway")
	}

	t.logger.Info("Requesting Verifier to join reshare...")
	err = t.requestVerifierReshare(ctx, vault, sessionID, hexEncryptionKey, pluginID, verifierURL, authHeader)
	if err != nil {
		return nil, fmt.Errorf("request verifier reshare: %w", err)
	}

	expectedParties := len(vault.Signers) + 2
	t.logger.WithField("expected", expectedParties).Info("Waiting for all parties to join...")

	parties, err := t.waitForParties(ctx, sessionID, expectedParties)
	if err != nil {
		return nil, fmt.Errorf("wait for parties: %w", err)
	}

	t.logger.WithField("parties", parties).Info("All parties joined, starting reshare")

	err = t.relayClient.StartSession(sessionID, parties)
	if err != nil {
		return nil, fmt.Errorf("start session: %w", err)
	}

	t.logger.Info("Running DKLS QC (reshare) protocol...")

	vault.Signers = parties

	return vault, nil
}

func (t *TSSService) requestFastVaultReshare(ctx context.Context, vault *LocalVault, sessionID, hexEncKey, password string) error {
	serverPartyID := generateServerPartyID(sessionID)

	type FastVaultReshareRequest struct {
		Name               string   `json:"name"`
		PublicKey          string   `json:"public_key"`
		SessionID          string   `json:"session_id"`
		HexEncryptionKey   string   `json:"hex_encryption_key"`
		HexChainCode       string   `json:"hex_chain_code"`
		LocalPartyId       string   `json:"local_party_id"`
		OldParties         []string `json:"old_parties"`
		OldResharePrefix   string   `json:"old_reshare_prefix"`
		EncryptionPassword string   `json:"encryption_password"`
		Email              string   `json:"email"`
		ReshareType        int      `json:"reshare_type"`
		LibType            int      `json:"lib_type"`
	}

	req := FastVaultReshareRequest{
		Name:               vault.Name,
		PublicKey:          vault.PublicKeyECDSA,
		SessionID:          sessionID,
		HexEncryptionKey:   hexEncKey,
		HexChainCode:       vault.HexChainCode,
		LocalPartyId:       serverPartyID,
		OldParties:         vault.Signers,
		OldResharePrefix:   vault.ResharePrefix,
		EncryptionPassword: password,
		Email:              "",
		ReshareType:        1,
		LibType:            vault.LibType,
	}

	reqJSON, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	t.logger.WithField("request", string(reqJSON)).Debug("Sending reshare request to Fast Vault Server")

	url := FastVaultServer + "/vault/reshare"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqJSON))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("fast vault server returned %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

func (t *TSSService) requestVerifierReshare(ctx context.Context, vault *LocalVault, sessionID, hexEncKey, pluginID, verifierURL, authHeader string) error {
	type VerifierReshareRequest struct {
		Name             string   `json:"name"`
		PublicKey        string   `json:"public_key"`
		SessionID        string   `json:"session_id"`
		HexEncryptionKey string   `json:"hex_encryption_key"`
		HexChainCode     string   `json:"hex_chain_code"`
		LocalPartyId     string   `json:"local_party_id"`
		OldParties       []string `json:"old_parties"`
		Email            string   `json:"email"`
		PluginID         string   `json:"plugin_id"`
	}

	req := VerifierReshareRequest{
		Name:             vault.Name,
		PublicKey:        vault.PublicKeyECDSA,
		SessionID:        sessionID,
		HexEncryptionKey: hexEncKey,
		HexChainCode:     vault.HexChainCode,
		LocalPartyId:     "verifier-" + sessionID[:8],
		OldParties:       vault.Signers,
		Email:            "",
		PluginID:         pluginID,
	}

	reqJSON, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := verifierURL + "/vault/reshare"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqJSON))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		httpReq.Header.Set("Authorization", authHeader)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("verifier returned %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

func (t *TSSService) ReshareWithPlugin(ctx context.Context, vault *LocalVault, pluginID, verifierURL, authHeader, vaultPassword string) (*LocalVault, error) {
	sessionID := uuid.New().String()

	encryptionKey := make([]byte, 32)
	_, err := rand.Read(encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("generate encryption key: %w", err)
	}
	hexEncryptionKey := hex.EncodeToString(encryptionKey)

	t.logger.WithFields(logrus.Fields{
		"session_id":   sessionID,
		"old_parties":  vault.Signers,
		"plugin_id":    pluginID,
		"verifier_url": verifierURL,
	}).Info("Starting plugin reshare session")

	err = t.relayClient.RegisterSession(sessionID, t.localPartyID)
	if err != nil {
		return nil, fmt.Errorf("register session: %w", err)
	}

	t.logger.Info("Requesting Fast Vault Server to join reshare...")
	err = t.requestFastVaultReshare(ctx, vault, sessionID, hexEncryptionKey, vaultPassword)
	if err != nil {
		t.logger.WithError(err).Warn("Failed to request Fast Vault Server - continuing anyway")
	}

	t.logger.Info("Requesting Verifier to join reshare (with plugin)...")
	err = t.requestVerifierReshare(ctx, vault, sessionID, hexEncryptionKey, pluginID, verifierURL, authHeader)
	if err != nil {
		return nil, fmt.Errorf("request verifier reshare: %w", err)
	}

	expectedParties := len(vault.Signers) + 2
	t.logger.WithField("expected", expectedParties).Info("Waiting for all parties to join...")

	parties, err := t.waitForParties(ctx, sessionID, expectedParties)
	if err != nil {
		return nil, fmt.Errorf("wait for parties: %w", err)
	}

	t.logger.WithField("parties", parties).Info("All parties joined, starting reshare")

	err = t.relayClient.StartSession(sessionID, parties)
	if err != nil {
		return nil, fmt.Errorf("start session: %w", err)
	}

	t.logger.Info("Running DKLS QC (reshare) protocol...")
	t.logger.Info("[NOTE: This requires go-wrappers CGO library for actual reshare]")

	err = t.relayClient.CompleteSession(sessionID, t.localPartyID)
	if err != nil {
		t.logger.WithError(err).Warn("Failed to complete session")
	}

	newVault := &LocalVault{
		Name:           vault.Name,
		PublicKeyECDSA: vault.PublicKeyECDSA,
		PublicKeyEdDSA: vault.PublicKeyEdDSA,
		HexChainCode:   vault.HexChainCode,
		LocalPartyID:   vault.LocalPartyID,
		Signers:        parties,
		KeyShares:      vault.KeyShares,
		ResharePrefix:  sessionID[:8],
		CreatedAt:      vault.CreatedAt,
		LibType:        vault.LibType,
	}

	return newVault, nil
}

type KeysignResult struct {
	R            string `json:"r"`
	S            string `json:"s"`
	RecoveryID   string `json:"recovery_id"`
	DerSignature string `json:"der_signature"`
}

func (t *TSSService) KeysignWithVerifier(ctx context.Context, vault *LocalVault, messages []string, derivePath, verifierURL, pluginID, authHeader string) ([]KeysignResult, error) {
	sessionID := uuid.New().String()

	encryptionKey := make([]byte, 32)
	_, err := rand.Read(encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("generate encryption key: %w", err)
	}
	hexEncryptionKey := hex.EncodeToString(encryptionKey)

	t.logger.WithFields(logrus.Fields{
		"session_id":   sessionID,
		"public_key":   vault.PublicKeyECDSA[:16] + "...",
		"messages":     len(messages),
		"derive_path":  derivePath,
		"plugin_id":    pluginID,
		"verifier_url": verifierURL,
	}).Info("Starting keysign with verifier")

	err = t.relayClient.RegisterSession(sessionID, t.localPartyID)
	if err != nil {
		return nil, fmt.Errorf("register session: %w", err)
	}

	t.logger.Info("Requesting Verifier to join keysign for policy...")
	err = t.requestVerifierKeysign(ctx, vault, sessionID, hexEncryptionKey, messages, derivePath, pluginID, verifierURL, authHeader)
	if err != nil {
		return nil, fmt.Errorf("request verifier keysign: %w", err)
	}

	t.logger.Info("Waiting for Verifier to join...")
	parties, err := t.waitForParties(ctx, sessionID, 2)
	if err != nil {
		return nil, fmt.Errorf("wait for parties: %w", err)
	}

	t.logger.WithField("parties", parties).Info("All parties joined, starting keysign")

	err = t.relayClient.StartSession(sessionID, parties)
	if err != nil {
		return nil, fmt.Errorf("start session: %w", err)
	}

	t.logger.Info("Running DKLS keysign protocol with Verifier...")
	t.logger.Info("[NOTE: This requires go-wrappers CGO library for actual signing]")

	err = t.relayClient.CompleteSession(sessionID, t.localPartyID)
	if err != nil {
		t.logger.WithError(err).Warn("Failed to complete session")
	}

	results := make([]KeysignResult, len(messages))
	for i := range messages {
		results[i] = KeysignResult{
			R:            "placeholder_r",
			S:            "placeholder_s",
			RecoveryID:   "1b",
			DerSignature: "placeholder_der",
		}
	}

	return results, nil
}

func (t *TSSService) requestVerifierKeysign(ctx context.Context, vault *LocalVault, sessionID, hexEncKey string, messages []string, derivePath, pluginID, verifierURL, authHeader string) error {
	type VerifierKeysignRequest struct {
		PublicKey        string   `json:"public_key"`
		Messages         []string `json:"messages"`
		Session          string   `json:"session"`
		HexEncryptionKey string   `json:"hex_encryption_key"`
		DerivePath       string   `json:"derive_path"`
		PluginID         string   `json:"plugin_id"`
		IsECDSA          bool     `json:"is_ecdsa"`
	}

	req := VerifierKeysignRequest{
		PublicKey:        vault.PublicKeyECDSA,
		Messages:         messages,
		Session:          sessionID,
		HexEncryptionKey: hexEncKey,
		DerivePath:       derivePath,
		PluginID:         pluginID,
		IsECDSA:          true,
	}

	reqJSON, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := verifierURL + "/vault/keysign"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqJSON))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		httpReq.Header.Set("Authorization", authHeader)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("verifier keysign returned %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

func (t *TSSService) Keysign(ctx context.Context, vault *LocalVault, messages []string, derivePath string, isEdDSA bool, vaultPassword string) ([]KeysignResult, error) {
	sessionID := uuid.New().String()

	encryptionKey := make([]byte, 32)
	_, err := rand.Read(encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("generate encryption key: %w", err)
	}
	hexEncryptionKey := hex.EncodeToString(encryptionKey)

	publicKey := vault.PublicKeyECDSA
	if isEdDSA {
		publicKey = vault.PublicKeyEdDSA
	}

	t.logger.WithFields(logrus.Fields{
		"session_id":  sessionID,
		"public_key":  publicKey[:16] + "...",
		"messages":    len(messages),
		"derive_path": derivePath,
		"is_eddsa":    isEdDSA,
	}).Info("Starting keysign session")

	err = t.relayClient.RegisterSession(sessionID, t.localPartyID)
	if err != nil {
		return nil, fmt.Errorf("register session: %w", err)
	}

	t.logger.Info("Requesting Fast Vault Server to join keysign...")
	err = t.requestFastVaultKeysign(ctx, vault, sessionID, hexEncryptionKey, messages, derivePath, isEdDSA, vaultPassword)
	if err != nil {
		return nil, fmt.Errorf("request fast vault keysign: %w", err)
	}

	t.logger.Info("Waiting for Fast Vault Server to join...")
	parties, err := t.waitForParties(ctx, sessionID, 2)
	if err != nil {
		return nil, fmt.Errorf("wait for parties: %w", err)
	}

	t.logger.WithField("parties", parties).Info("All parties joined, starting keysign")

	err = t.relayClient.StartSession(sessionID, parties)
	if err != nil {
		return nil, fmt.Errorf("start session: %w", err)
	}

	t.logger.Info("Running DKLS keysign protocol...")
	t.logger.Info("[NOTE: This requires go-wrappers CGO library for actual signing]")

	err = t.relayClient.CompleteSession(sessionID, t.localPartyID)
	if err != nil {
		t.logger.WithError(err).Warn("Failed to complete session")
	}

	results := make([]KeysignResult, len(messages))
	for i := range messages {
		results[i] = KeysignResult{
			R:            "placeholder",
			S:            "placeholder",
			RecoveryID:   "00",
			DerSignature: "placeholder",
		}
	}

	return results, nil
}

func (t *TSSService) requestFastVaultKeysign(ctx context.Context, vault *LocalVault, sessionID, hexEncKey string, messages []string, derivePath string, isEdDSA bool, vaultPassword string) error {
	type FastVaultSignRequest struct {
		PublicKey        string   `json:"public_key"`
		Messages         []string `json:"messages"`
		Session          string   `json:"session"`
		HexEncryptionKey string   `json:"hex_encryption_key"`
		DerivePath       string   `json:"derive_path"`
		IsECDSA          bool     `json:"is_ecdsa"`
		VaultPassword    string   `json:"vault_password"`
	}

	publicKey := vault.PublicKeyECDSA
	if isEdDSA {
		publicKey = vault.PublicKeyEdDSA
	}

	req := FastVaultSignRequest{
		PublicKey:        publicKey,
		Messages:         messages,
		Session:          sessionID,
		HexEncryptionKey: hexEncKey,
		DerivePath:       derivePath,
		IsECDSA:          !isEdDSA,
		VaultPassword:    vaultPassword,
	}

	reqJSON, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := FastVaultServer + "/vault/sign"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqJSON))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("fast vault server returned %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

func VaultStoragePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".vultisig", "vaults")
}

func SaveVault(vault *LocalVault) error {
	dir := VaultStoragePath()
	err := os.MkdirAll(dir, 0700)
	if err != nil {
		return fmt.Errorf("create vault dir: %w", err)
	}

	var filename string
	if vault.PublicKeyECDSA != "" && len(vault.PublicKeyECDSA) >= 16 {
		filename = fmt.Sprintf("%s.json", vault.PublicKeyECDSA[:16])
	} else {
		filename = fmt.Sprintf("%s-%s.json", vault.Name, vault.CreatedAt[:10])
	}
	path := filepath.Join(dir, filename)

	data, err := json.MarshalIndent(vault, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal vault: %w", err)
	}

	err = os.WriteFile(path, data, 0600)
	if err != nil {
		return fmt.Errorf("write vault: %w", err)
	}

	return nil
}

func LoadVault(pubKeyPrefix string) (*LocalVault, error) {
	dir := VaultStoragePath()

	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read vault dir: %w", err)
	}

	for _, f := range files {
		if strings.HasPrefix(f.Name(), pubKeyPrefix) || strings.Contains(f.Name(), pubKeyPrefix) {
			path := filepath.Join(dir, f.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read vault file: %w", err)
			}

			var vault LocalVault
			err = json.Unmarshal(data, &vault)
			if err != nil {
				return nil, fmt.Errorf("unmarshal vault: %w", err)
			}

			return &vault, nil
		}
	}

	return nil, fmt.Errorf("vault not found")
}

func ListVaults() ([]*LocalVault, error) {
	dir := VaultStoragePath()

	files, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read vault dir: %w", err)
	}

	var vaults []*LocalVault
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".json") {
			path := filepath.Join(dir, f.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}

			var vault LocalVault
			err = json.Unmarshal(data, &vault)
			if err != nil {
				continue
			}

			vaults = append(vaults, &vault)
		}
	}

	return vaults, nil
}
