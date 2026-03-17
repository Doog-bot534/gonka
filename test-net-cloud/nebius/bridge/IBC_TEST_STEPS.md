# IBC / LP & Community Sale Test Steps (Testnet)

End-to-end test plan for testnet: **register LP, fund LP, register and fund community sale, then run upgrade 11** (which migrates community sale to new code and registers the new code ID). After upgrade, the contract can be used (e.g. pay/purchase).

---

## Prerequisites

- Testnet node RPC (e.g. `http://node1.gonka.ai:8000/chain-rpc/` or `http://89.169.111.79:26657`)
- Key with balance for deposits and fees (e.g. `gonka-account-key`)
- **Governance module address** for your chain (use in proposals and as contract admin).  
  Example mainnet: `gonka10d07y265gmmuvt4z0w9aw880jnsr700j2h5m33`.  
  On testnet, get it from an existing governance proposal or chain params.

---

## Phase 1: Before upgrade — Liquidity Pool

1. **Register Liquidity Pool**  
   - Use the same flow as mainnet: register LP (store + instantiate or use existing code ID) via governance.  
   - Scripts: `bridge-pool-register.sh` (see [BRIDGE_TESTNET_GUIDE.md](./BRIDGE_TESTNET_GUIDE.md) §4).  
   - Verify: proposal passes and LP address is registered (`inferenced q inference registered-liquidity-pool-address`).

2. **Fund Liquidity Pool from community pool**  
   - Governance proposal: CommunityPoolSpend to the **registered LP contract address**.  
   - Script: `bridge-pool-fund.sh` (see BRIDGE_TESTNET_GUIDE §5).  
   - Verify: LP balance and community pool balance.

---

## Phase 2: Before upgrade — Community Sale

3. **Get community-sale WASM (v1 compatible with mainnet proposal 14)**  
   - From repo: `inference-chain/contracts/community-sale/artifacts/community_sale.wasm`  
   - Or from mainnet: contract code from [node1.gonka.ai dashboard](http://node1.gonka.ai:8000/dashboard/gonka/cosmwasm/14/contracts) / GitHub release.

4. **Store community-sale WASM on testnet**  
   - Upload the WASM (same version as mainnet for “before upgrade” state):
     ```bash
     inferenced tx wasm store inference-chain/contracts/community-sale/artifacts/community_sale.wasm \
       --from gonka-account-key --chain-id <CHAIN_ID> --node <RPC> \
       --keyring-backend file --home .inference --gas auto --gas-adjustment 1.3 \
       --broadcast-mode sync --output json --yes
     ```
   - Note the returned **code_id** (e.g. `3`).

5. **Instantiate community-sale contract**  
   - Use **governance module address** as `admin` and for `--admin` (so upgrades can be done via governance).  
   - New contract schema requires `accepted_ibc_denom`; use a placeholder if IBC is not set up yet (e.g. `"ibc/placeholder"`), or the real IBC denom.
   - Example (replace `GOV_ADDR`, `BUYER_ADDR`, `CODE_ID`, `CHAIN_ID`, `RPC`):
     ```bash
     inferenced tx wasm instantiate 4 \
       '{"admin":"gonka10d07y265gmmuvt4z0w9aw880jnsr700j2h5m33","buyer":"gonka1u6tf76c80snq05642msrkyez4a39u979upp8w5","accepted_chain_id":"ethereum","accepted_eth_contract":"0xdac17f958d2ee523a2206206994597c13d831ec7","accepted_ibc_denom":"ibc/placeholder","price_usd":"25000"}' \
       --label "community-sale-testnet" \
       --admin gonka10d07y265gmmuvt4z0w9aw880jnsr700j2h5m33 \
       --from gonka-account-key --chain-id gonka-testnet --node http://89.169.111.79:26657 \
       --keyring-backend file --home .inference --gas auto --gas-adjustment 1.3 \
       --broadcast-mode sync --output json --yes
     ```
   - Note the **contract address** from the tx response (needed for funding and for upgrade 11).

6. **Governance: fund community sale from community pool**  
   - Submit a governance proposal whose single message is **MsgCommunityPoolSpend**: recipient = **community-sale contract address** (from step 5), amount = desired GNK (e.g. 1M GNK).  
   - Deposit and vote so the proposal passes.  
   **Option A – use the script** (submit + vote in one go):
   ```bash
   ./bridge-community-sale-fund.sh --recipient gonka1wkwy0xh89ksdgj9hr347dyd2dw7zesmtrue6kfzyml4vdtz6e5wsms7nus
   # Optional: --amount 1000000000000000ngonka (default 1M GNK)
   ```

   **Option B – manual**: Message type `@type: /cosmos.distribution.v1beta1.MsgCommunityPoolSpend`, authority = gov module, recipient = `<CONTRACT_ADDRESS>`, amount = `[{ "denom": "ngonka", "amount": "1000000000000000" }]`; deposit e.g. 25000000ngonka.  

   Verify: `inferenced q bank balances <CONTRACT_ADDRESS>` and/or `inferenced q distribution community-pool`.

---

## Phase 3: Upgrade 11 — New community-sale code and migration

7. **Build and store the NEW community-sale WASM (v2 with IBC / allow_all_trade_tokens)**  
   - Build from repo (version that has the migrate handler and IBC support):
     ```bash
     cd inference-chain/contracts/community-sale && ./build.sh
     ```
   - Store the **new** WASM on testnet (before the upgrade runs):
     ```bash
     inferenced tx wasm store artifacts/community_sale.wasm \
       --from gonka-account-key --chain-id <CHAIN_ID> --node <RPC> \
       --keyring-backend file --home .inference --gas auto --gas-adjustment 1.3 \
       --broadcast-mode sync --output json --yes
     ```
   - Note the **new code_id** (e.g. `5`). This is **new_code_id** for the upgrade.

8. **Submit software-upgrade proposal (v0.2.11) with migration data in upgrade-info**  
   - The upgrade handler in v0.2.11 reads **Plan.Info** as JSON and expects:
     - `community_sale_address` — the existing community-sale contract address (from step 5)
     - `new_code_id` — the code ID from step 7
   - Include these in the same **upgrade-info** JSON as your binaries (so the chain has the contract address at upgrade time).  
   - Example (replace placeholders; single-line JSON for shell):
     ```bash
     inferenced tx upgrade software-upgrade v0.2.11 \
       --title "Upgrade Proposal v0.2.11" \
       --upgrade-height <HEIGHT> \
       --chain-id <CHAIN_ID> \
       --upgrade-info '{
         "binaries": {"linux/amd64": "https://.../inferenced-amd64.zip?checksum=sha256:..."},
         "api_binaries": {"linux/amd64": "https://.../decentralized-api-amd64.zip?checksum=sha256:..."},
         "community_sale_address": "<CONTRACT_ADDRESS_FROM_STEP_5>",
         "new_code_id": <NEW_CODE_ID_FROM_STEP_7>
       }' \
       --summary "Upgrade v0.2.11 + migrate community sale to new code" \
       --deposit 50000000ngonka \
       --from gonka-account-key --yes --broadcast-mode sync --output json \
       --gas auto --gas-adjustment 1.5 \
       --keyring-backend file --home .inference --node <RPC>
     ```
   - At the upgrade height, the handler will:
     - Run normal v0.2.11 migrations.
     - Call CosmWasm migrate on `community_sale_address` to `new_code_id` with payload `{"allow_all_trade_tokens": true}`.

9. **Vote and wait for upgrade**  
   - Vote yes on the upgrade proposal; wait until the chain halts at the upgrade height and restarts with the new binary.  
   - Verify: chain runs new version; community-sale contract is migrated (query contract config and confirm it’s on the new code / has `allow_all_trade_tokens` if exposed).

---

## Phase 4: After upgrade — Use the contract

10. **Pay / use the contract**  
    - Buyer: send CW20 (or, if supported, IBC) to the community-sale contract per contract interface (e.g. CW20 Send with purchase hook).  
    - Optionally run a small purchase and check balances and contract state.

---

## Checklist summary

| Step | What |
|------|------|
| 1–2  | Register LP → Fund LP (governance) |
| 3–4  | Get v1 community-sale WASM → Store on testnet, note code_id |
| 5    | Instantiate community-sale, note **contract address** |
| 6    | Governance: CommunityPoolSpend to **contract address** (fund sale) |
| 7    | Build & store **new** community-sale WASM, note **new_code_id** |
| 8    | Submit v0.2.11 upgrade proposal with **upgrade-info** containing `community_sale_address` + `new_code_id` (+ binaries) |
| 9    | Vote, let upgrade run |
| 10   | Use contract (pay/purchase) |

---

## References

- LP and bridge: [BRIDGE_TESTNET_GUIDE.md](./BRIDGE_TESTNET_GUIDE.md)
- Community sale contract: [inference-chain/contracts/community-sale/README.md](../../../inference-chain/contracts/community-sale/README.md)
- Migration and upgrade: [proposals/ibc-pool-trade-support/migration.md](../../../proposals/ibc-pool-trade-support/migration.md), [inference-chain/app/upgrades/v0_2_11/upgrades.go](../../../inference-chain/app/upgrades/v0_2_11/upgrades.go)
- Mainnet community sale (proposal 14): http://node1.gonka.ai:8000/dashboard/gonka/cosmwasm/14/contracts
