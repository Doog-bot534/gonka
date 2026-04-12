set -e

./stop.sh

# Don't need to make path relative to ./local-test-net, becayse make is run with root as workdir
export GENESIS_OVERRIDES_FILE="inference-chain/test_genesis_overrides.json"
export BLST_PORTABLE=1
export SET_LATEST=1
make -C ../. build-docker

# Build the standalone devshardd host binary that versiond runs as a versioned
# child via the VERSIOND_OVERRIDE_<name> mechanism. The docker-compose.versiond.yml
# bind-mounts ../build/devshardd into the versiond container, so this file
# must exist on the host before any test that uses that compose file.
make -C ../. devshardd-local-build
