#!/bin/bash
set -euo pipefail

K3S_TOKEN="${K3S_TOKEN:-}"
MASTER_IP="${MASTER_IP:-$(hostname -I | awk '{print $1}')}"

if [ -z "$K3S_TOKEN" ]; then
    echo "Error: K3S_TOKEN environment variable required"
    exit 1
fi

echo "=== Installing k3s master node ==="
echo "Master IP: $MASTER_IP"

# Install dependencies
apt-get update
apt-get install -y curl wget open-iscsi nfs-common

# Install k3s with WireGuard for cross-region networking
curl -sfL https://get.k3s.io | sh -s - server \
    --token "$K3S_TOKEN" \
    --node-label "topology.kubernetes.io/region=fsn1" \
    --node-label "node-role.kubernetes.io/master=true" \
    --flannel-backend=wireguard-native \
    --disable traefik \
    --disable servicelb \
    --write-kubeconfig-mode 644 \
    --tls-san "$MASTER_IP" \
    --cluster-cidr=10.42.0.0/16 \
    --service-cidr=10.43.0.0/16

# Wait for k3s to be ready
echo "Waiting for k3s to be ready..."
sleep 30

# Check node status
kubectl wait --for=condition=ready node --all --timeout=300s

echo ""
echo "=== Master node installed ==="
echo "Kubeconfig: /etc/rancher/k3s/k3s.yaml"
