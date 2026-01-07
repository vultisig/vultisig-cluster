#!/bin/bash
# Wrapper script for vcli that auto-sets DYLD_LIBRARY_PATH from cluster.yaml
#
# This is needed because the DKLS library path is baked into the binary at
# compile time. macOS requires DYLD_LIBRARY_PATH to override the embedded path.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VCLI_BIN="$SCRIPT_DIR/vcli"

# If DYLD_LIBRARY_PATH is already set, just run vcli
if [ -n "$DYLD_LIBRARY_PATH" ]; then
    exec "$VCLI_BIN" "$@"
fi

# Try to find cluster.yaml and extract dyld_path
CLUSTER_YAML=""
for path in "$SCRIPT_DIR/cluster.yaml" "$SCRIPT_DIR/../local/cluster.yaml" "$HOME/.vultisig/cluster.yaml"; do
    if [ -f "$path" ]; then
        CLUSTER_YAML="$path"
        break
    fi
done

if [ -z "$CLUSTER_YAML" ]; then
    echo "Warning: cluster.yaml not found, DYLD_LIBRARY_PATH may not be set correctly" >&2
    exec "$VCLI_BIN" "$@"
fi

# Extract dyld_path from cluster.yaml (simple grep approach)
DYLD_PATH=$(grep 'dyld_path:' "$CLUSTER_YAML" | cut -d: -f2 | tr -d ' ' | sed "s|~|$HOME|g")

if [ -z "$DYLD_PATH" ]; then
    echo "Warning: dyld_path not found in cluster.yaml" >&2
    exec "$VCLI_BIN" "$@"
fi

# Run vcli with DYLD_LIBRARY_PATH set
export DYLD_LIBRARY_PATH="$DYLD_PATH"
exec "$VCLI_BIN" "$@"
