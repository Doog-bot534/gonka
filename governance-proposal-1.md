## Draft

./inferenced tx gov draft-proposal --from node-4 --keyring-backend file --gas 2000000 --node http://node2.gonka.ai:8000/chain-rpc/ --home ~/mainnet


## Submit

./inferenced tx gov \
    submit-proposal ./draft_proposal.json \
    --from node-4 \
    --keyring-backend file \
    --unordered \
    --timeout-duration=60s \
    --gas=2000000 \
    --gas-adjustment=5.0 \
    --node http://node2.gonka.ai:8000/chain-rpc/ \
    --yes \
    --home ~/mainnet \
    --chain-id gonka-mainnet


## List Proposals
./inferenced query gov proposals --node http://node2.gonka.ai:8000/chain-rpc/

./inferenced query gov tally 2 --node http://node2.gonka.ai:8000/chain-rpc/


./inferenced tx gov vote 5 yes \
  --from node-1 \
  --keyring-backend file \
  --unordered --timeout-duration=60s \
  --gas=2000000 --gas-adjustment=5.0 \
  --node http://node2.gonka.ai:8000/chain-rpc/ \
  --home ~/mainnet \
   --chain-id gonka-mainnet \
   --yes

-----

## Node Management

### Delete node1
```bash
curl -X DELETE http://204.12.169.238:9200/admin/v1/nodes/node1 | jq
```

### Add node1 back
```bash
curl -X POST http://204.12.169.238:9200/admin/v1/nodes \
-H "Content-Type: application/json" \
-d '{
  "host": "82.141.118.3",
  "inference_segment": "",
  "inference_port": 6079,
  "poc_segment": "",
  "poc_port": 6088,
  "models": {
    "Qwen/Qwen3-4B-Instruct-2507": {
      "args": [
        "--quantization",
        "fp8",
        "--gpu-memory-utilization",
        "0.9"
      ]
    }
  },
  "id": "node1",
  "max_concurrent": 500,
  "hardware": [],
  "version": ""
}' | jq
```