# Vultisig Cluster

Development and testing environment for Vultisig services. Supports two modes:

1. **Local Development** - Docker-based, runs services from source code
2. **Kubernetes Cluster** - Multi-region k3s on Hetzner Cloud for network testing

## Prerequisites

- **Go 1.23+** - https://go.dev/dl/
- **Docker** - https://docs.docker.com/get-docker/
- **Docker Compose** - Usually included with Docker Desktop

## Dependencies

Clone all required repositories into the same parent directory:

```bash
mkdir -p ~/dev/vultisig && cd ~/dev/vultisig

# This repo
git clone https://github.com/vultisig/vultisig-cluster.git

# Required dependencies
git clone https://github.com/vultisig/verifier.git
git clone https://github.com/vultisig/app-recurring.git
git clone https://github.com/vultisig/go-wrappers.git
```

### Building go-wrappers (DKLS library)

The go-wrappers repo contains the native DKLS cryptographic library required for TSS operations:

```bash
cd ~/dev/vultisig/go-wrappers

# macOS
./build_darwin.sh

# Linux
./build_linux.sh
```

This creates the native library in `includes/darwin/` (macOS) or `includes/linux/` (Linux).

**Note:** The library path must be configured in `local/cluster.yaml` under `library.dyld_path`.

## Quick Reference

| Goal | Command |
|------|---------|
| Local dev with Docker | `make local-start` |
| K8s with custom relay/vultiserver | `make deploy-k8s` |
| K8s with production endpoints | `make deploy-k8s-prod` |

---

## Local Development (Docker)

Runs services locally from source code with Docker for infrastructure (Postgres, Redis, MinIO).

### Setup

```bash
# 1. Configure paths to your local repos
cp local/cluster.yaml.example local/cluster.yaml
# Edit cluster.yaml with your repo paths (adjust ~/dev/vultisig to your location)

# 2. Configure vault credentials (for testing)
cp local/vault.env.example local/vault.env
# Edit vault.env with:
#   VAULT_PATH=/path/to/your/vault-backup.vult
#   VAULT_PASSWORD=your-password

# 3. Start everything
make local-start
```

### Vault Requirement

You need a **Fast Vault** (vault with cloud backup) exported from the Vultisig mobile app:

1. Create a vault in the Vultisig mobile app with "Fast Vault" enabled
2. Export the vault backup (Settings → Export → Backup file)
3. Transfer the `.vult` file to your development machine
4. Configure the path in `local/vault.env`

### Service Modes

In `local/cluster.yaml`, each service can be `local` or `production`:

```yaml
services:
  relay: production      # Use api.vultisig.com/router
  vultiserver: production  # Use api.vultisig.com
  verifier: local        # Run from source
  dca_server: local      # Run from source
```

This lets you test local changes against production relay/vultiserver, or run everything locally.

### vcli - Development CLI

The `vcli` tool manages vaults, plugins, and policies for local testing:

```bash
# Import a vault
./local/vcli.sh vault import -f /path/to/vault.bak -p "password" --force

# Install a plugin
./local/vcli.sh plugin install vultisig-dca-0000 -p "password"

# Create a policy
./local/vcli.sh policy create -p vultisig-dca-0000 -c local/configs/test-policy.json --password "password"

# Check status
./local/vcli.sh report
./local/vcli.sh policy status <policy-id>

# Uninstall plugin
./local/vcli.sh plugin uninstall vultisig-dca-0000
```

See [local/VCLI.md](local/VCLI.md) for detailed usage.

### Local Commands

```bash
make local-start    # Build and start all services
make local-stop     # Stop all services
make local-status   # Show service status
make local-logs     # Tail all logs
make local-clean    # Remove binaries and configs
```

---

## Kubernetes Cluster (Hetzner)

Multi-region k3s cluster for testing TSS behavior, network partitions, and production-like deployments.

### Architecture

```
Hetzner Cloud
├── fsn1 (Falkenstein) - Master + Worker
├── nbg1 (Nuremberg)   - Worker
└── hel1 (Helsinki)    - Worker

Namespaces:
├── infra/          PostgreSQL, Redis, MinIO
├── relay/          TSS message routing (optional)
├── verifier/       Verifier API + Worker
├── plugin-dca/     DCA Server, Worker, Scheduler, TX Indexer
├── vultiserver/    Fast Vault API (optional)
└── monitoring/     Prometheus, Grafana
```

### Deployment Modes

**Option 1: Custom Relay + VultiServer** (`make deploy-k8s`)
- Deploys relay and vultiserver in the cluster
- Full control over all services
- Use for testing relay/vultiserver changes

**Option 2: Production Endpoints** (`make deploy-k8s-prod`)
- Uses `https://api.vultisig.com/router` for relay
- Uses `https://api.vultisig.com` for vultiserver
- Lighter deployment, only verifier + DCA + infra

### Setup

```bash
# 1. Provision Hetzner VMs
cd infrastructure/terraform
cp terraform.tfvars.example terraform.tfvars
# Edit with your Hetzner API token
cd ../..
make init
make apply

# 2. Install k3s cluster
make cluster-setup

# 3. Configure secrets
cp k8s/secrets-template.yaml k8s/secrets.yaml
# Edit secrets.yaml with actual values

# 4. Deploy
make deploy-k8s       # With custom relay/vultiserver
# OR
make deploy-k8s-prod  # With production endpoints
```

### Network Partition Testing

Test TSS behavior under network failures:

```bash
# Isolate relay (TSS should fail)
make partition-isolate-relay

# Isolate worker
make partition-isolate-worker

# Restore connectivity
make partition-restore

# Interactive test
./tests/network-partition-test.sh test-tss-partition
```

### K8s Commands

```bash
make status           # Cluster status
make logs-verifier    # Tail verifier logs
make logs-worker      # Tail worker logs
make logs-relay       # Tail relay logs
make logs-dca-worker  # Tail DCA worker logs
make port-forward     # Access services locally
make destroy          # Tear down infrastructure
```

---

## Directory Structure

```
vultisig-cluster/
├── local/                    # Local development
│   ├── cmd/devctl/           # CLI source code
│   ├── vcli.sh               # CLI wrapper script
│   ├── cluster.yaml.example  # Config template
│   ├── vault.env.example     # Vault config template
│   └── configs/              # Test policy configs
├── infrastructure/
│   ├── terraform/            # Hetzner VM provisioning
│   └── scripts/              # k3s installation
├── k8s/
│   ├── base/                 # Core manifests
│   │   ├── infra/            # Postgres, Redis, MinIO
│   │   ├── verifier/         # Verifier stack
│   │   ├── dca/              # DCA plugin stack
│   │   ├── relay/            # Relay service
│   │   ├── vultiserver/      # VultiServer
│   │   └── monitoring/       # Prometheus, Grafana
│   └── overlays/
│       ├── local/            # Includes relay + vultiserver
│       └── production/       # Uses api.vultisig.com
├── tests/                    # Test scripts
└── Makefile
```

---

## Secrets

Generate secrets:
```bash
openssl rand -hex 16  # passwords
openssl rand -hex 32  # encryption keys
```

Required secrets:
- PostgreSQL credentials
- Redis password
- MinIO credentials
- Encryption key (32 bytes)
- JWT signing secret
- GHCR token (for pulling images)

---

## Comparison

| Feature | Local (Docker) | K8s Custom | K8s Production |
|---------|---------------|------------|----------------|
| Infrastructure | Docker containers | k3s pods | k3s pods |
| Relay | Configurable | In-cluster | api.vultisig.com |
| VultiServer | Configurable | In-cluster | api.vultisig.com |
| Code changes | Hot reload | Rebuild image | N/A |
| Network testing | Limited | Full control | Limited |
| Use case | Development | Integration/Network tests | Staging |
