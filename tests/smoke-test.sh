#!/bin/bash
set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}PASS${NC}: $1"; }
fail() { echo -e "${RED}FAIL${NC}: $1"; }
warn() { echo -e "${YELLOW}WARN${NC}: $1"; }

echo "=== Vultisig Cluster Smoke Test ==="
echo ""

# Check kubectl connection
echo "Checking cluster connection..."
kubectl cluster-info &>/dev/null && pass "Cluster reachable" || { fail "Cannot connect to cluster"; exit 1; }

# Check nodes
echo ""
echo "Checking nodes..."
NODE_COUNT=$(kubectl get nodes --no-headers | wc -l | tr -d ' ')
if [ "$NODE_COUNT" -ge 4 ]; then
    pass "Found $NODE_COUNT nodes"
else
    warn "Expected 4 nodes, found $NODE_COUNT"
fi

kubectl get nodes -o wide

# Check infrastructure
echo ""
echo "Checking infrastructure services..."

check_pod() {
    local ns=$1
    local label=$2
    local name=$3
    if kubectl -n "$ns" get pods -l "$label" --no-headers 2>/dev/null | grep -q "Running"; then
        pass "$name running"
        return 0
    else
        fail "$name not running"
        return 1
    fi
}

check_pod infra app=postgres "PostgreSQL"
check_pod infra app=redis "Redis"
check_pod infra app=minio "MinIO"

# Check relay
echo ""
echo "Checking relay..."
check_pod relay app=relay "Relay"

# Check verifier stack
echo ""
echo "Checking verifier stack..."
check_pod verifier "app=verifier,component=api" "Verifier API"
check_pod verifier "app=verifier,component=worker" "Verifier Worker"
check_pod verifier "app=verifier,component=tx-indexer" "Verifier TX Indexer"

# Check DCA plugin
echo ""
echo "Checking DCA plugin..."
check_pod plugin-dca "app=dca,component=server-swap" "DCA Server (swap)"
check_pod plugin-dca "app=dca,component=server-send" "DCA Server (send)"
check_pod plugin-dca "app=dca,component=worker" "DCA Worker"
check_pod plugin-dca "app=dca,component=scheduler" "DCA Scheduler"
check_pod plugin-dca "app=dca,component=tx-indexer" "DCA TX Indexer"

# Check vultiserver
echo ""
echo "Checking vultiserver..."
check_pod vultiserver "app=vultiserver,component=api" "VultiServer API"
check_pod vultiserver "app=vultiserver,component=worker" "VultiServer Worker"

# Check monitoring
echo ""
echo "Checking monitoring..."
check_pod monitoring app=prometheus "Prometheus"
check_pod monitoring app=grafana "Grafana"

# Test endpoints
echo ""
echo "Testing service endpoints..."

test_endpoint() {
    local ns=$1
    local svc=$2
    local port=$3
    local path=$4
    local name=$5

    local result
    result=$(kubectl -n "$ns" run curl-test-$RANDOM --rm -i --restart=Never --image=curlimages/curl -- \
        -sf -o /dev/null -w "%{http_code}" "http://${svc}:${port}${path}" 2>/dev/null || echo "000")

    if [ "$result" = "200" ]; then
        pass "$name endpoint (HTTP $result)"
    else
        fail "$name endpoint (HTTP $result)"
    fi
}

test_endpoint relay relay 8080 /ping "Relay"
test_endpoint verifier verifier 8080 /healthz "Verifier API"

echo ""
echo "=== Smoke test complete ==="
