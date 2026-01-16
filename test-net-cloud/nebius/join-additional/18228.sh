export KEY_NAME="join-18228"
export PUBLIC_URL="http://xj7-5.s.filfox.io:19256"
export P2P_EXTERNAL_ADDRESS="tcp://xj7-5.s.filfox.io:19255"
export SYNC_WITH_SNAPSHOTS="false"
export DAPI_API__POC_CALLBACK_URL="http://172.18.114.103:9100"
export HF_HOME="/srv/dai/cache/"
export TESTNET_BASE_DIR="/srv/dai/"
python3 launch.py --mode join --branch origin/testnet/main
