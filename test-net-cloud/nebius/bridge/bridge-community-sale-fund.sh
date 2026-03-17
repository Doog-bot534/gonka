#!/bin/bash
set -e

# Fund Community Sale contract from community pool (governance proposal + vote).
# Usage:
#   ./bridge-community-sale-fund.sh --recipient <CONTRACT_ADDRESS> [--amount 1000000000000000ngonka] [--password PASS] [--proposal ID]
# Or via SSH: ssh user@host "bash -s" -- < bridge-community-sale-fund.sh --recipient <ADDR> [--amount ...]

export BASE_DIR="${TESTNET_BASE_DIR:-/srv/dai}"
if [ -f "$BASE_DIR/inferenced" ]; then
    export APP_NAME="$BASE_DIR/inferenced"
else
    export APP_NAME="inferenced"
fi

export KEY_DIR="$BASE_DIR/.inference"
export CHAIN_ID="${CHAIN_ID:-gonka-testnet}"
export KEY_NAME="${KEY_NAME:-gonka-account-key}"
export NODE_OPTS="${NODE_OPTS:---node http://localhost:8000/chain-rpc/}"

get_keyring_backend() {
    local pass=$1
    export KEYRING_BACKEND=""
    if printf "%s\n" "$pass" | $APP_NAME keys show "$KEY_NAME" --keyring-backend "file" --keyring-dir "$KEY_DIR" >/dev/null 2>&1; then
        export KEYRING_BACKEND="file"
        echo "Found key '$KEY_NAME' in 'file' backend."
        return 0
    fi
    if printf "%s\n" "$pass" | $APP_NAME keys show "$KEY_NAME" --keyring-backend "test" --keyring-dir "$KEY_DIR" >/dev/null 2>&1; then
        export KEYRING_BACKEND="test"
        echo "Found key '$KEY_NAME' in 'test' backend."
        return 0
    fi
    echo "Error: Key '$KEY_NAME' not found."
    return 1
}

echo "=================================================="
echo "Fund Community Sale from Community Pool"
echo "Binary:  $APP_NAME"
echo "Key:     $KEY_NAME"
echo "=================================================="

PASSWORD="${PASSWORD:-12345678}"
# 1M GNK (1e6 * 1e9 base units)
AMOUNT="${AMOUNT:-1000000000000000ngonka}"
RECIPIENT=""
PROPOSAL_ID_ARG=""

while [[ $# -gt 0 ]]; do
  case $1 in
    --recipient)
      RECIPIENT="$2"
      shift 2
      ;;
    --amount)
      AMOUNT="$2"
      shift 2
      ;;
    --password)
      PASSWORD="$2"
      shift 2
      ;;
    --proposal)
      PROPOSAL_ID_ARG="$2"
      shift 2
      ;;
    *)
      echo "Error: Unknown option $1"
      echo "Usage: $0 --recipient <CONTRACT_ADDRESS> [--amount AMT] [--password PASS] [--proposal ID]"
      exit 1
      ;;
  esac
done

get_keyring_backend "$PASSWORD" || exit 1

if [ -z "$PROPOSAL_ID_ARG" ]; then
    if [ -z "$RECIPIENT" ]; then
        echo "Error: --recipient <CONTRACT_ADDRESS> is required when creating a new proposal."
        exit 1
    fi

    echo "Recipient (community-sale contract): $RECIPIENT"
    echo "Amount: $AMOUNT"

    GOV_ACCOUNT_JSON=$($APP_NAME q auth module-account gov --output json $NODE_OPTS </dev/null)
    AUTHORITY_ADDRESS=$(echo "$GOV_ACCOUNT_JSON" | jq -r '.account.value.address // .account.base_account.address // empty')
    if [ -z "$AUTHORITY_ADDRESS" ]; then
        echo "Error: Could not fetch gov module address."
        exit 1
    fi

    VAL=$(echo "$AMOUNT" | sed 's/[^0-9]//g')
    DENOM=$(echo "$AMOUNT" | sed 's/[0-9]//g')

    PROPOSAL_FILE="/tmp/proposal_fund_community_sale.json"
    jq -n \
      --arg auth "$AUTHORITY_ADDRESS" \
      --arg recipient "$RECIPIENT" \
      --arg denom "$DENOM" \
      --arg val "$VAL" \
      '{
        messages: [
          {
            "@type": "/cosmos.distribution.v1beta1.MsgCommunityPoolSpend",
            authority: $auth,
            recipient: $recipient,
            amount: [{ denom: $denom, amount: $val }]
          }
        ],
        deposit: "25000000ngonka",
        title: "Fund Community Sale Contract (testnet)",
        summary: "Transfer GNK from community pool to community sale contract",
        metadata: "https://github.com/gonka-ai/gonka"
      }' > "$PROPOSAL_FILE"

    echo "Submitting Proposal..."
    RAW_SUBMIT_OUT=$(printf "%s\n%s\n" "$PASSWORD" "$PASSWORD" | $APP_NAME tx gov submit-proposal "$PROPOSAL_FILE" \
      --from "$KEY_NAME" --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --yes --output json \
      --keyring-backend "$KEYRING_BACKEND" --home "$BASE_DIR/.inference" $NODE_OPTS 2>&1)
    SUBMIT_OUT=$(echo "$RAW_SUBMIT_OUT" | sed -n '/{/,$p')
    TX_HASH=$(echo "$SUBMIT_OUT" | jq -r '.txhash' 2>/dev/null || echo "null")

    if [ "$TX_HASH" == "null" ] || [ -z "$TX_HASH" ]; then
        echo "Error: Submit-proposal failed."
        echo "$RAW_SUBMIT_OUT"
        exit 1
    fi
    echo "TX Hash: $TX_HASH"
    echo "Waiting 6 seconds..."
    sleep 6
    PROPOSAL_ID=$($APP_NAME q gov proposals --output json $NODE_OPTS </dev/null | jq -r '.proposals[-1].id')
else
    PROPOSAL_ID="$PROPOSAL_ID_ARG"
fi

echo "Proposal ID: $PROPOSAL_ID"
if [ -z "$PROPOSAL_ID" ] || [ "$PROPOSAL_ID" == "null" ]; then
    echo "Error: Could not find proposal ID."
    exit 1
fi

echo "Voting YES..."
MAX_RETRIES=5
RETRY_COUNT=0
VOTE_SUCCESS=false
while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
    VOTE_OUT=$(printf "%s\n%s\n" "$PASSWORD" "$PASSWORD" | $APP_NAME tx gov vote "$PROPOSAL_ID" yes \
      --from "$KEY_NAME" --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --yes --output json \
      --keyring-backend "$KEYRING_BACKEND" --home "$BASE_DIR/.inference" $NODE_OPTS 2>&1)
    if echo "$VOTE_OUT" | grep -q '"code":0' || echo "$VOTE_OUT" | grep -q "txhash"; then
        echo "$VOTE_OUT"
        VOTE_SUCCESS=true
        break
    fi
    echo "Vote attempt $((RETRY_COUNT+1)) failed."
    RETRY_COUNT=$((RETRY_COUNT+1))
    sleep 5
done

if [ "$VOTE_SUCCESS" = true ]; then
    echo "Proposal submitted and voted. Check status in ~1 minute; then verify contract balance."
else
    echo "Error: Failed to vote."
    exit 1
fi
echo "Done!"
