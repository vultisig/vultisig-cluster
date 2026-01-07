# VCli Development Notes

Development CLI for managing the Vultisig local development environment.

## Quick Start

```bash
# Build and start everything
make local-start

# Import vault (uses vault.env)
set -a && source ./local/vault.env && set +a
./local/vcli.sh vault import -f "$VAULT_PATH" -p "$VAULT_PASSWORD" --force

# Install plugin
./local/vcli.sh plugin install vultisig-dca-0000 -p "$VAULT_PASSWORD"

# Create policy
./local/vcli.sh policy create -p vultisig-dca-0000 -c local/configs/test-one-time-policy.json --password "$VAULT_PASSWORD"

# Check status
./local/vcli.sh report
./local/vcli.sh policy status <policy-id>

# Stop everything
make local-stop
```

## E2E Testing Checklist

1. **Start services**: `make local-start`
2. **Import vault**:
   ```bash
   set -a && source ./local/vault.env && set +a
   ./local/vcli.sh vault import -f "$VAULT_PATH" -p "$VAULT_PASSWORD" --force
   ```
3. **Install plugin**: `./local/vcli.sh plugin install vultisig-dca-0000 -p "$VAULT_PASSWORD"`
4. **Create policy**: `./local/vcli.sh policy create -p vultisig-dca-0000 -c <config.json> --password "$VAULT_PASSWORD"`
5. **VERIFY EXECUTION**: Wait 30s for scheduler, then check:
   ```bash
   ./local/vcli.sh policy status <policy-id>
   ./local/vcli.sh policy transactions <policy-id>
   tail -f /tmp/dca-worker.log
   ```
6. **Check overall status**: `./local/vcli.sh report`
7. **Uninstall**: `./local/vcli.sh plugin uninstall vultisig-dca-0000`
8. **Stop**: `make local-stop`

## Common Gotchas

### Environment Variables

Always use `set -a` to export environment variables:
```bash
# WRONG - variables not exported
source ./local/vault.env

# CORRECT - variables exported to subprocesses
set -a && source ./local/vault.env && set +a
```

### Billing Array

Use `"billing": []` for plugins with no pricing (like vultisig-dca-0000):
```json
{
  "recipe": { ... },
  "billing": []
}
```

If you get: `billing policies count (1) does not match plugin pricing count (0)`, your billing array doesn't match the plugin's pricing. Most test plugins have no fees.

### MinIO Access Denied

If keyshares show "Not found" or "Access Denied", the mc alias may not be configured:
```bash
docker exec vultisig-minio mc alias set local http://localhost:9000 minioadmin minioadmin
docker exec vultisig-minio mc ls local/vultisig-verifier/
```

### Scheduler Delay

The DCA scheduler polls every 30 seconds. For testing:
- Use `"frequency": "one-time"` for immediate execution
- Check worker logs: `tail -f /tmp/dca-worker.log`
- Use `policy trigger` to force immediate execution

### Policy Frequency Values

- `"one-time"` - Execute once immediately
- `"daily"` - Execute every 24 hours
- `"weekly"` - Execute every 7 days
- `"monthly"` - Execute every 30 days

### Rule Validation Errors

If you see errors like `tx target is wrong`, this is the security layer working:
- The policy rules validate that transactions match expected parameters
- This can happen when DEX router addresses change or get upgraded
- Check the rule target vs actual target in the error message

## Useful Commands

### Logs
```bash
tail -f /tmp/verifier.log      # Verifier server
tail -f /tmp/worker.log        # Verifier worker
tail -f /tmp/dca.log           # DCA plugin server
tail -f /tmp/dca-worker.log    # DCA plugin worker
```

### Database
```bash
# Connect to verifier DB
docker exec -it vultisig-postgres psql -U vultisig -d vultisig-verifier

# Connect to DCA DB
docker exec -it vultisig-postgres psql -U vultisig -d vultisig-dca

# Check plugin installations
docker exec vultisig-postgres psql -U vultisig -d vultisig-verifier -c \
  "SELECT plugin_id, public_key, installed_at FROM plugin_installations;"

# Check scheduler
docker exec vultisig-postgres psql -U vultisig -d vultisig-dca -c \
  "SELECT * FROM scheduler ORDER BY next_execution LIMIT 5;"

# Check transactions
docker exec vultisig-postgres psql -U vultisig -d vultisig-dca -c \
  "SELECT * FROM tx_indexer ORDER BY created_at DESC LIMIT 5;"
```

### Redis
```bash
docker exec vultisig-redis redis-cli -a vultisig KEYS '*'
```

### MinIO
```bash
# Console: http://localhost:9090 (minioadmin/minioadmin)

# List keyshares
docker exec vultisig-minio mc ls local/vultisig-verifier/
docker exec vultisig-minio mc ls local/vultisig-dca/
```

## Test Policy Examples

### ETH to USDC (one-time)
`local/configs/test-one-time-policy.json`:
```json
{
  "recipe": {
    "from": { "chain": "Ethereum", "token": "", "address": "0x..." },
    "to": { "chain": "Ethereum", "token": "0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48", "address": "0x..." },
    "fromAmount": "1000000000000000",
    "frequency": "one-time"
  },
  "billing": []
}
```

### USDT to BTC
`verifier/devenv/config/usdt-btc-policy.json`:
```json
{
  "recipe": {
    "from": { "chain": "Ethereum", "token": "0xdAC17F958D2ee523a2206206994597C13D831ec7", "address": "" },
    "to": { "chain": "Bitcoin", "token": "", "address": "" },
    "fromAmount": "100000000",
    "frequency": "one-time"
  },
  "billing": []
}
```

## Files

| File | Purpose |
|------|---------|
| `local/vcli` | Built binary |
| `local/vcli.sh` | Wrapper script (sets DYLD_LIBRARY_PATH) |
| `local/cluster.yaml` | Configuration (copy from .example) |
| `local/vault.env` | Vault credentials (copy from .example) |
| `local/configs/` | Test configuration files |
| `~/.vultisig/vaults/` | Local vault storage |
| `~/.vultisig/auth-token.json` | Authentication token cache |
