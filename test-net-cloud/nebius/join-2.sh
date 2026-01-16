export KEY_NAME="join-2"
export PUBLIC_URL="http://172.18.114.102:8000"
export P2P_EXTERNAL_ADDRESS="tcp://172.18.114.102:5000"
export SYNC_WITH_SNAPSHOTS="false"
export DAPI_API__POC_CALLBACK_URL="http://api:9100"
python3 launch.py --mode join --branch origin/testnet/main
