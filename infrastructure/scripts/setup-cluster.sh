#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Source environment if exists
if [ -f "$ROOT_DIR/setup-env.sh" ]; then
    source "$ROOT_DIR/setup-env.sh"
else
    echo "Error: Run 'terraform apply' first to generate setup-env.sh"
    exit 1
fi

SSH_OPTS="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null"
if [ -n "${SSH_KEY:-}" ]; then
    SSH_OPTS="-i $SSH_KEY $SSH_OPTS"
fi

echo "=== Vultisig k3s Cluster Setup ==="
echo ""

# Step 1: Install master
echo "[1/4] Installing k3s master on $MASTER_IP..."
ssh $SSH_OPTS root@"$MASTER_IP" "
    export K3S_TOKEN='$K3S_TOKEN'
    export MASTER_IP='$MASTER_IP'
    bash -s
" < "$SCRIPT_DIR/k3s-install-master.sh"

# Step 2: Get kubeconfig
echo ""
echo "[2/4] Fetching kubeconfig..."
mkdir -p "$ROOT_DIR/.kube"
scp $SSH_OPTS root@"$MASTER_IP":/etc/rancher/k3s/k3s.yaml "$ROOT_DIR/.kube/config"
sed -i.bak "s/127.0.0.1/$MASTER_IP/g" "$ROOT_DIR/.kube/config"
rm -f "$ROOT_DIR/.kube/config.bak"

export KUBECONFIG="$ROOT_DIR/.kube/config"

# Step 3: Install workers
echo ""
echo "[3/4] Installing worker nodes..."

for region in fsn1 nbg1 hel1; do
    var_name="WORKER_${region^^}_IP"
    WORKER_IP="${!var_name:-}"

    if [ -n "$WORKER_IP" ]; then
        echo "  Installing worker in $region ($WORKER_IP)..."
        ssh $SSH_OPTS root@"$WORKER_IP" "
            export K3S_TOKEN='$K3S_TOKEN'
            export MASTER_URL='$MASTER_PRIVATE_IP'
            export REGION='$region'
            bash -s
        " < "$SCRIPT_DIR/k3s-install-worker.sh"
    fi
done

# Step 4: Verify cluster
echo ""
echo "[4/4] Verifying cluster..."
sleep 10
kubectl get nodes -o wide

# Install Hetzner CSI driver for volumes
echo ""
echo "Installing Hetzner CSI driver..."
kubectl apply -f https://raw.githubusercontent.com/hetznercloud/csi-driver/main/deploy/kubernetes/hcloud-csi.yml

echo ""
echo "=== Cluster setup complete ==="
echo ""
echo "KUBECONFIG=$ROOT_DIR/.kube/config"
echo ""
echo "To use: export KUBECONFIG=$ROOT_DIR/.kube/config"
