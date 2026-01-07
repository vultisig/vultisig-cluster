#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
POLICIES_DIR="$SCRIPT_DIR/../k8s/network-policies/partition-tests"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

ACTION="${1:-help}"
TARGET="${2:-}"

case "$ACTION" in
    "isolate-region")
        if [ -z "$TARGET" ]; then
            echo "Usage: $0 isolate-region <region>"
            echo "Regions: fsn1, nbg1, hel1"
            exit 1
        fi
        echo -e "${YELLOW}Applying network isolation for region: $TARGET${NC}"
        kubectl apply -f "$POLICIES_DIR/isolate-region-${TARGET}.yaml"
        echo -e "${GREEN}Region $TARGET is now isolated from other regions${NC}"
        ;;

    "isolate-service")
        if [ -z "$TARGET" ]; then
            echo "Usage: $0 isolate-service <service>"
            echo "Services: relay, worker"
            exit 1
        fi
        echo -e "${YELLOW}Applying network isolation for service: $TARGET${NC}"
        kubectl apply -f "$POLICIES_DIR/isolate-${TARGET}.yaml"
        echo -e "${GREEN}Service $TARGET is now isolated${NC}"
        ;;

    "restore")
        echo -e "${YELLOW}Removing all partition test policies...${NC}"
        kubectl delete networkpolicy -l partition-test=true --all-namespaces 2>/dev/null || true
        echo -e "${GREEN}Network connectivity restored${NC}"
        ;;

    "status")
        echo "Current network policies with partition-test label:"
        kubectl get networkpolicy -l partition-test=true --all-namespaces 2>/dev/null || echo "None active"
        echo ""
        echo "All network policies:"
        kubectl get networkpolicy --all-namespaces
        ;;

    "test-tss-partition")
        echo "=== TSS Network Partition Test ==="
        echo ""
        echo "This test will:"
        echo "1. Isolate the relay service"
        echo "2. Attempt a keysign operation (should fail/timeout)"
        echo "3. Restore connectivity"
        echo "4. Retry keysign (should succeed)"
        echo ""
        read -p "Continue? [y/N] " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            exit 0
        fi

        echo -e "\n${YELLOW}Step 1: Isolating relay...${NC}"
        kubectl apply -f "$POLICIES_DIR/isolate-relay.yaml"

        echo -e "\n${YELLOW}Step 2: Relay isolated. TSS operations should now fail.${NC}"
        echo "Check logs: kubectl -n verifier logs -l component=worker -f"
        echo ""
        read -p "Press enter when ready to restore..."

        echo -e "\n${YELLOW}Step 3: Restoring relay connectivity...${NC}"
        kubectl delete -f "$POLICIES_DIR/isolate-relay.yaml"

        echo -e "\n${GREEN}Relay restored. TSS operations should now succeed.${NC}"
        ;;

    *)
        echo "Network Partition Test Tool"
        echo ""
        echo "Usage: $0 <action> [options]"
        echo ""
        echo "Actions:"
        echo "  isolate-region <region>     Isolate a region (fsn1, nbg1, hel1)"
        echo "  isolate-service <service>   Isolate a service (relay, worker)"
        echo "  restore                     Restore all network connectivity"
        echo "  status                      Show current network policies"
        echo "  test-tss-partition          Interactive TSS partition test"
        echo ""
        echo "Examples:"
        echo "  $0 isolate-region fsn1      # Partition Falkenstein region"
        echo "  $0 isolate-service relay    # Block relay service"
        echo "  $0 restore                  # Restore all connectivity"
        ;;
esac
