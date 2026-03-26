#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

"${SCRIPT_DIR}/stop.sh"

# This path is consumed by the Docker build context, so it must stay repo-relative.
export GENESIS_OVERRIDES_FILE="inference-chain/test_genesis_overrides.json"
export BLST_PORTABLE=1
export SET_LATEST=1
make -C "${REPO_ROOT}" build-docker
