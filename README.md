# Vultisig Cluster

Multi-region Kubernetes cluster for testing Vultisig services with network partition simulation.

## Architecture

```
Hetzner Cloud
├── fsn1 (Falkenstein) - Master + Worker
├── nbg1 (Nuremberg)   - Worker
└── hel1 (Helsinki)    - Worker

Services:
├── infra/          PostgreSQL, Redis, MinIO
├── relay/          TSS message routing
├── verifier/       Verifier API, Worker, TX Indexer
├── plugin-dca/     DCA Server, Worker, Scheduler, TX Indexer
├── vultiserver/    Fast Vault API, Worker
└── monitoring/     Prometheus, Grafana
```

## Quick Start

### 1. Prerequisites

- Terraform >= 1.0
- kubectl
- SSH key (will be generated if not provided)

### 2. Configure Terraform

```bash
cd infrastructure/terraform
cp terraform.tfvars.example terraform.tfvars
# Edit terraform.tfvars with your Hetzner API token
```

### 3. Provision Infrastructure

```bash
make init
make apply
```

### 4. Setup k3s Cluster

```bash
make cluster-setup
```

### 5. Configure Secrets

```bash
cp k8s/secrets-template.yaml k8s/secrets.yaml
# Edit k8s/secrets.yaml with actual values
```

### 6. Deploy Services

```bash
make deploy-all
```

### 7. Verify Deployment

```bash
make test-smoke
```

## Network Partition Testing

Test TSS behavior under network failures:

```bash
# Isolate relay service (TSS should fail)
make partition-isolate-relay

# Isolate verifier worker
make partition-isolate-worker

# Restore connectivity
make partition-restore

# Interactive TSS partition test
./tests/network-partition-test.sh test-tss-partition
```

## Common Commands

```bash
# View cluster status
make status

# Tail logs
make logs-verifier
make logs-worker
make logs-relay

# Port forward for local access
make port-forward
# Then access:
#   Verifier:   http://localhost:8080
#   Grafana:    http://localhost:3000
#   Prometheus: http://localhost:9090

# Destroy everything
make destroy
```

## Directory Structure

```
vultisig-cluster/
├── infrastructure/
│   ├── terraform/       # Hetzner VM provisioning
│   └── scripts/         # k3s installation scripts
├── k8s/
│   ├── infra/           # PostgreSQL, Redis, MinIO
│   ├── relay/           # Relay service
│   ├── verifier/        # Verifier stack
│   ├── dca/             # DCA plugin stack
│   ├── vultiserver/     # VultiServer
│   ├── monitoring/      # Prometheus, Grafana
│   └── network-policies/# Partition test policies
├── tests/               # Test scripts
└── Makefile
```

## Secrets Required

| Secret | Description |
|--------|-------------|
| PostgreSQL password | Database access |
| Redis password | Cache/queue access |
| MinIO credentials | Object storage |
| Encryption secret | 32-byte key for vault encryption |
| JWT secret | API authentication |

Generate secrets:
```bash
openssl rand -hex 16  # passwords
openssl rand -hex 32  # encryption keys
```
