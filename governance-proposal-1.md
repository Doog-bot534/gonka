## Current Params
./inferenced query inference params -o json --node $SEED_URL/chain-rpc/ > current_params.json



## Draft

./inferenced tx gov draft-proposal --from node-2 --keyring-backend file --gas 2000000 --node $SEED_URL/chain-rpc/ --home ./glebnet-nebius


## Submit

./inferenced tx gov \
    submit-proposal ./draft_proposal.json \
    --from node-1 \
    --keyring-backend file \
    --unordered \
    --timeout-duration=60s \
    --gas=2000000 \
    --gas-adjustment=5.0 \
    --node $SEED_URL/chain-rpc/ \
    --yes \
    --home ./glebnet-nebius \
    --chain-id gonka-mainnet


## List Proposals
./inferenced query gov proposals --node $SEED_URL/chain-rpc/

./inferenced query gov tally 2 --node $SEED_URL/chain-rpc/


./inferenced tx gov vote 2 yes \
  --from node-1 \
  --keyring-backend file \
  --unordered --timeout-duration=60s \
  --gas=2000000 --gas-adjustment=5.0 \
  --node $SEED_URL/chain-rpc/ \
  --home ./glebnet-nebius \
   --chain-id gonka-mainnet