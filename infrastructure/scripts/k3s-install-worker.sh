#!/bin/bash
set -euo pipefail

K3S_TOKEN="${K3S_TOKEN:-}"
MASTER_URL="${MASTER_URL:-}"
REGION="${REGION:-unknown}"

if [ -z "$K3S_TOKEN" ]; then
    echo "Error: K3S_TOKEN environment variable required"
    exit 1
fi

if [ -z "$MASTER_URL" ]; then
    echo "Error: MASTER_URL environment variable required"
    exit 1
fi

echo "=== Installing k3s worker node ==="
echo "Region: $REGION"
echo "Master URL: $MASTER_URL"

# Install dependencies
apt-get update
apt-get install -y curl wget open-iscsi nfs-common

# Install k3s agent
curl -sfL https://get.k3s.io | K3S_URL="https://${MASTER_URL}:6443" K3S_TOKEN="$K3S_TOKEN" sh -s - agent \
    --node-label "topology.kubernetes.io/region=$REGION" \
    --node-label "node-role.kubernetes.io/worker=true"

echo ""
echo "=== Worker node joined cluster ==="
