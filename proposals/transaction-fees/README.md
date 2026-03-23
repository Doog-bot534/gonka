# Proposal: Transaction Fees for Spam Prevention

This proposal introduces consensus-level transaction fees to the Gonka network. A governance-controlled minimum gas price is enforced via a custom `TxFeeChecker` wired into the existing `DeductFeeDecorator`, with a bypass mechanism that exempts protocol-duty messages from fees. The goal is to make transaction spam economically infeasible without impacting legitimate network operations.

## 1. Summary of Changes

The Gonka network currently operates with zero transaction fees. Gas prices are explicitly set to `0ngonka` in the decentralized API client and transaction manager. While this simplifies participation, it leaves the general transaction surface --- governance, bank sends, staking, collateral, reward claims, bridge operations, and CosmWasm calls --- with no economic friction preventing abuse.

This proposal introduces three changes:

1. **On-chain `FeeParams.min_gas_price` parameter** stored in `x/inference` module params, adjustable via governance proposal without chain upgrade.
2. **Custom `TxFeeChecker`** that reads the on-chain parameter and enforces it at consensus level (both `CheckTx` and `DeliverTx`), replacing the current `nil` fee checker.
3. **`NetworkDutyFeeBypassDecorator`** that exempts protocol-obligation messages (PoC submissions, validations, BLS messages, weight distributions) from fees, following the pattern established by the existing `LiquidityPoolFeeBypassDecorator`.

Account creation remains out of scope and will be addressed separately.

## 2. Context

### 2.1. Existing Spam Prevention

The network already has several non-economic spam prevention layers:

*   **Transfer Agent whitelist**: Only 7 allowlisted addresses may submit `MsgStartInference` / `MsgFinishInference` (configured in `app/upgrades/v0_2_9/upgrades.go`).
*   **Developer access gating**: Restricts inference requests to allowlisted developers during early phases (`DeveloperAccessParams`).
*   **Participant blocklist/allowlist**: Controls who may submit PoC and participate in epochs (`ParticipantBlocklistParams`, KVStore allowlist).
*   **PoC window validation** (`PocPeriodValidationDecorator` in `ante_poc_period.go`): Rejects PoC messages outside allowed time windows.
*   **Validation duplicate check** (`ValidationEarlyRejectDecorator` in `ante_validation.go`): Prevents duplicate validations and verifies subgroup membership.
*   **Bandwidth limiting** (`BandwidthLimitsParams`): Chain-wide cap on concurrent inferences and invalidations per block.

These layers are effective for their targeted domains. The gap is the general transaction surface: governance proposals, bank sends, staking operations, collateral management, reward claims, bridge operations, and CosmWasm contract calls have no economic cost.

### 2.2. Why Per-Validator `minimum-gas-prices` Is Insufficient

The Cosmos SDK `minimum-gas-prices` setting in `app.toml` is per-validator and only enforced during `CheckTx` (mempool admission). It is **not** enforced during `DeliverTx` / `FinalizeBlock`. A block proposer that sets `minimum-gas-prices = ""` can include zero-fee transactions in blocks, bypassing other validators' mempool filters. This is a well-documented weakness (Cosmos SDK issues [#4527](https://github.com/cosmos/cosmos-sdk/issues/4527), [#8224](https://github.com/cosmos/cosmos-sdk/discussions/8224), [#12269](https://github.com/cosmos/cosmos-sdk/issues/12269)).

Gonka currently passes `nil` as the `TxFeeChecker` to `DeductFeeDecorator` (`ante.go` line 210), which falls through to the default `checkTxFeeWithValidatorMinGasPrices` --- the per-validator check described above.

### 2.3. Current Fee Configuration

Three locations explicitly set zero fees:

*   `decentralized-api/cosmosclient/cosmosclient.go` lines 121-122: `WithGasPrices("0ngonka")`, `WithFees("0ngonka")`
*   `decentralized-api/cosmosclient/tx_manager/tx_manager.go` lines 917-920: `WithGasPrices("")`, `WithFees("")`
*   `decentralized-api/cosmosclient/tx_manager/tx_manager.go` lines 940-941: `SetGasLimit(10000000000000)`, `SetFeeAmount(sdk.Coins{})`

The batch path is particularly concerning: it sets gas limits to 10^13 with empty fee amounts, meaning any batch transaction consumes an enormous block gas budget at zero cost.

## 3. Proposed Solution

### 3.1. Design Principles

1.  **Consensus enforcement.** Fees are enforced during both `CheckTx` and `DeliverTx` to prevent proposer manipulation. This requires a custom `TxFeeChecker`, not per-validator `app.toml` configuration.
2.  **Governance control.** The minimum gas price is an on-chain parameter adjustable via governance proposal. No chain upgrade is needed for price adjustments.
3.  **Exempt network duties.** Transactions that nodes must submit as protocol obligations are fee-exempt. These are already incentivized through the reward system and penalized through slashing.
4.  **Recursive safety.** All message-type checks recursively unpack `x/authz` `MsgExec` wrappers to prevent bypass via nested execution, following the pattern in `PocPeriodValidationDecorator`.
5.  **Minimal disruption.** The implementation hooks into existing SDK extension points (`HandlerOptions.TxFeeChecker`) and follows the established bypass decorator pattern (`LiquidityPoolFeeBypassDecorator`). No SDK fork required.

### 3.2. Fee-Exempt Message Types

These are protocol obligations. Nodes must submit them to participate, and each already has a non-fee throttling mechanism:

| Message | Duty | Existing Throttle |
|---------|------|-------------------|
| `MsgSubmitPocBatch` | PoC participation | `PocPeriodValidationDecorator` window check |
| `MsgSubmitPocValidation` | PoC validation | `PocPeriodValidationDecorator` window check |
| `MsgSubmitPocValidationsV2` | PoC V2 validation | `PocPeriodValidationDecorator` window check |
| `MsgPoCV2StoreCommit` | PoC V2 off-chain commit | `PocPeriodValidationDecorator` window check |
| `MsgPoCV2StoreReveal` | PoC V2 off-chain reveal | Window check |
| `MsgValidation` | Inference validation | `ValidationEarlyRejectDecorator` duplicate/subgroup check |
| `MsgMLNodeWeightDistribution` | Weight distribution | `PocPeriodValidationDecorator` window check |
| `MsgSubmitDealerPart` | BLS DKG round | Epoch-scoped |
| `MsgSubmitVerificationVector` | BLS DKG verification | Epoch-scoped |
| `MsgSubmitReconstructedKey` | BLS DKG reconstruction | Epoch-scoped |
| `MsgSubmitSignature` | BLS threshold signature | Per-request |

### 3.3. Fee-Required Message Types

All messages not in the exempt set require fees. Key categories:

| Category | Messages | Notes |
|----------|----------|-------|
| Inference | `MsgStartInference`, `MsgFinishInference` | Already gated by TA whitelist + escrow. Fee negligible vs escrow. |
| Inference challenges | `MsgInvalidateInference`, `MsgRevalidateInference` | Already throttled by bandwidth limits. Fee adds friction. |
| Rewards | `MsgClaimRewards` | Prevents per-block no-op claims. Reward far exceeds fee. |
| Staking | `MsgDelegate`, `MsgUndelegate`, `MsgBeginRedelegate` | Standard Cosmos anti-spam. |
| Governance | `MsgSubmitProposal`, `MsgVote`, `MsgDeposit` | Prevents governance spam. |
| Collateral | `MsgDepositCollateral`, `MsgWithdrawCollateral` | Prevents deposit/withdraw cycling. |
| Bank | `MsgSend`, `MsgMultiSend` | Prevents transfer spam. |
| CosmWasm | `MsgExecuteContract` (non-LP) | LP swaps already bypassed by existing decorator. |
| Bridge | `MsgWrapTokens`, `MsgUnwrapTokens`, `MsgBridgeExchange` | Prevents bridge abuse. |
| Training | `MsgCreateTrainingTask`, `MsgJoinTrainingTask`, etc. | Prevents training spam. |
| Participant | `MsgSubmitNewParticipant` | One-time, but fee prevents registration spam. |
| Admin | `MsgUpdateParams`, `MsgRegisterModel`, etc. | Authority-gated; fee adds defense-in-depth. |

## 4. Pricing Analysis

### 4.1. Denomination

1 GNK = 1,000,000,000 ngonka (10^9). The base denomination on chain is `ngonka`.

### 4.2. Fee Comparison Table

At current market price of GNK = $0.5698, with a typical transaction consuming ~80,000 gas:

| Min Gas Price | Fee per Tx (ngonka) | Fee per Tx (USD) | 10k Spam Attack | 100k Spam Attack |
|---|---|---|---|---|
| 1 ngonka | 80,000 | $0.000046 | $0.46 | $4.56 |
| **10 ngonka** | **800,000** | **$0.00046** | **$4.56** | **$45.58** |
| 100 ngonka | 8,000,000 | $0.0046 | $45.58 | $455.84 |
| 1,000 ngonka | 80,000,000 | $0.046 | $455.84 | $4,558.40 |

### 4.3. Comparison to Inference Costs

A typical inference escrow at post-grace pricing:

```
max_tokens = 5,000
prompt_tokens = 500
per_token_price = 100 ngonka (base price after grace period)
escrow = (5,000 + 500) × 100 = 550,000 ngonka ≈ $0.00031
```

At `10ngonka` gas price, the transaction fee (~800,000 ngonka, ~$0.00046) is comparable to the minimum inference escrow. For legitimate users making a single request, this is immaterial. For an attacker flooding 100,000 transactions, it costs ~$45.

### 4.4. Recommended Initial Value

`FeeParams.min_gas_price = 10ngonka`

This puts individual transactions well under a cent while making sustained spam cost real money. The parameter is governance-adjustable and should be tuned based on observed mainnet behavior and GNK price movements.

## 5. Implementation Details

### 5.1. On-Chain Parameter

Add `FeeParams` to `x/inference` module params:

```protobuf
// In inference-chain/proto/inference/inference/params.proto
message FeeParams {
  // Minimum gas price enforced at consensus level.
  // Denominated in ngonka. Governance-adjustable.
  cosmos.base.v1beta1.DecCoin min_gas_price = 1;
}
```

Accessor function in `inference-chain/x/inference/keeper/params.go`:

```
GetMinGasPrice(ctx) → DecCoin
```

Default value: `{denom: "ngonka", amount: "10"}`.

### 5.2. Custom TxFeeChecker

Implement `NewGonkaFeeChecker()` in `inference-chain/app/ante.go`. This replaces the current `nil` fee checker passed to `DeductFeeDecorator`:

```
NewGonkaFeeChecker(inferenceKeeper):
    1. Read min_gas_price from chain state via inferenceKeeper.GetMinGasPrice(ctx)
    2. If bypass flag is set on context (from NetworkDutyFeeBypassDecorator), skip check
    3. Calculate required_fee = min_gas_price.amount × tx.gas_limit
    4. If tx.fee < required_fee, reject with "insufficient fee" error
    5. Calculate priority from gas price (higher fee = higher priority)
    6. Return (effective_fee, priority, nil)
```

Wire into the ante handler by passing it to `HandlerOptions.TxFeeChecker`:

```
ante.HandlerOptions{
    ...
    TxFeeChecker: NewGonkaFeeChecker(options.InferenceKeeper),
}
```

Because `TxFeeChecker` is called inside `DeductFeeDecorator`, it runs during both `CheckTx` and `DeliverTx`, providing consensus-level enforcement. A malicious block proposer cannot include zero-fee transactions.

### 5.3. Network Duty Fee Bypass Decorator

Implement `NetworkDutyFeeBypassDecorator` in `inference-chain/app/ante.go`, following the `LiquidityPoolFeeBypassDecorator` pattern.

**Behavior:**

1.  Check if *all* messages in the transaction are in the exempt set (see Section 3.2).
2.  Recursively unpack `x/authz` `MsgExec` wrappers. If any inner message is not exempt, the entire transaction requires fees (fail closed).
3.  If all messages are exempt: clear `ctx.MinGasPrices()`, set a context flag for the `TxFeeChecker` to skip its check, enforce gas cap, and optionally boost priority.
4.  If any message is not exempt: pass through without modification.

**Recursive unpacking** follows the pattern already established in `PocPeriodValidationDecorator` (`ante_poc_period.go`):

```
isNetworkDutyRecursive(msg):
    if msg is MsgExec:
        for each inner_msg in msg.GetMessages():
            if not isNetworkDutyRecursive(inner_msg):
                return false
        return true
    return isExemptMessageType(msg)
```

**Parameters:**

*   `GasCap`: `1,000,000` gas. Prevents abuse where a node submits duty transactions with inflated gas that consume excessive block space without paying fees.
*   `Priority`: `500,000`. Ensures zero-fee duty transactions are not starved in the mempool.

### 5.4. Ante Handler Chain

Insert the new decorator immediately before `DeductFeeDecorator`, alongside the existing `LiquidityPoolFeeBypassDecorator`:

```
anteDecorators:
    1.  SetUpContextDecorator                        // existing
    2.  LimitSimulationGasDecorator                  // existing
    3.  CountTXDecorator                             // existing
    4.  GasRegisterDecorator                         // existing
    5.  CircuitBreakerDecorator                      // existing
    6.  ExtensionOptionsDecorator                    // existing
    7.  ValidateBasicDecorator                       // existing
    8.  TxTimeoutHeightDecorator                     // existing
    9.  ValidateMemoDecorator                        // existing
    10. ConsumeGasForTxSizeDecorator                 // existing
    11. LiquidityPoolFeeBypassDecorator              // existing
    12. NetworkDutyFeeBypassDecorator                // NEW
    13. DeductFeeDecorator (with NewGonkaFeeChecker) // MODIFIED
    14. PocPeriodValidationDecorator                 // existing
    15. ValidationEarlyRejectDecorator               // existing
    16. SetPubKeyDecorator                           // existing
    17. ValidateSigCountDecorator                    // existing
    18. SigGasConsumeDecorator                       // existing
    19. SigVerificationDecorator                     // existing
    20. IncrementSequenceDecorator                   // existing
    21. RedundantRelayDecorator                      // existing
```

All other ante handler logic remains unchanged.

### 5.5. DAPI Client Updates

**Single-transaction path** (`decentralized-api/cosmosclient/cosmosclient.go`):

```
// Before:
WithGasPrices("0ngonka")
WithFees("0ngonka")

// After:
WithGasPrices("10ngonka")
// Remove WithFees — let gas simulation determine fee from gas price
```

**Batch transaction path** (`decentralized-api/cosmosclient/tx_manager/tx_manager.go`):

```
// Before (lines 917-920):
WithGasAdjustment(10)
WithFees("")
WithGasPrices("")
WithGas(0)

// After:
WithGasAdjustment(10)
WithGasPrices("10ngonka")
WithGas(0)

// Before (lines 940-941):
unsignedTx.SetGasLimit(10000000000000)
unsignedTx.SetFeeAmount(sdk.Coins{})

// After:
unsignedTx.SetGasLimit(gasEstimate)   // from simulation
// SetFeeAmount derived from gasEstimate × min gas price
```

Since the DAPI primarily submits network-duty messages (validations, PoC batches, weight distributions), the bypass decorator covers the majority of its transaction volume. For the few fee-required messages (`MsgStartInference`, `MsgFinishInference`), the fee is negligible compared to the inference escrow.

### 5.6. Fee Revenue Flow

Collected fees flow through the existing Cosmos SDK infrastructure. No new distribution logic is needed:

```
Fee Payer Account
  → DeductFeeDecorator
    → fee_collector module account
      → x/distribution module (EndBlocker)
        → Validators (commission)
        → Delegators (staking rewards)
```

This aligns validator incentives with network security: validators earn more by including legitimate fee-paying transactions.

### 5.7. Feegrant Integration

The `x/feegrant` module is already wired into `DeductFeeDecorator` (`options.FeegrantKeeper`). Once fees are non-zero, feegrant becomes useful:

*   **DAPI operational grants**: A service account can grant fee allowances to the DAPI's operational wallets using `AllowedMsgAllowance` restricted to specific message types.
*   **User onboarding**: New users with no tokens can have fees paid by a granter until they acquire tokens.

No additional implementation is needed.

## 6. Rollout

### 6.1. Single-Phase Chain Upgrade

Because per-validator `minimum-gas-prices` is `CheckTx`-only, a phased validator-by-validator rollout would create inconsistent behavior and does not provide consensus-level protection. This proposal ships as a single coordinated chain upgrade:

1.  Implement `FeeParams`, `NewGonkaFeeChecker`, and `NetworkDutyFeeBypassDecorator`.
2.  Update DAPI gas price configuration in `cosmosclient.go` and `tx_manager.go`.
3.  Add testermint tests verifying fee enforcement, bypass behavior, and `MsgExec` recursive unpacking.
4.  Submit governance upgrade proposal with target block height.
5.  All validators upgrade simultaneously. Fees enforced uniformly from activation block.

### 6.2. Post-Activation Tuning

`FeeParams.min_gas_price` is governance-adjustable:

*   If spam persists → increase via governance proposal.
*   If legitimate usage is impacted → decrease via governance proposal.
*   If GNK price moves significantly → adjust to maintain target USD cost per transaction.

No chain upgrade needed for parameter changes.

## 7. Security Analysis

### 7.1. Consensus-Level Enforcement

The custom `TxFeeChecker` runs inside `DeductFeeDecorator` during both `CheckTx` and `DeliverTx`. Unlike per-validator `minimum-gas-prices`, a malicious block proposer cannot include zero-fee transactions because they will be rejected during block execution.

### 7.2. MsgExec Wrapping Attack

Without recursive unpacking, an attacker could wrap a fee-required message (e.g., `MsgSend`) inside `x/authz` `MsgExec` to bypass fees. The `isNetworkDutyRecursive` function prevents this by unpacking all nested messages and failing closed on any non-exempt inner message. This follows the same pattern used by `PocPeriodValidationDecorator` in `ante_poc_period.go`.

### 7.3. Gas Cap on Bypassed Transactions

The `GasCap` (1,000,000 gas) on the bypass decorator prevents abuse where a node submits duty transactions with inflated gas that consume excessive block space without paying fees.

### 7.4. Mixed Transaction Prevention

Requiring *all* messages in a transaction to be network duties prevents bundling spam alongside duty messages to avoid fees.

### 7.5. Fee Bypass List Criteria

Adding a message to the exempt list means it can be submitted for free. Each addition must satisfy:

1.  The message is a **protocol obligation** --- nodes must submit it to participate.
2.  There is **already a non-fee throttling mechanism** (timing windows, duplicate check, allowlist).
3.  Free submission **cannot be exploited** for economic gain or network degradation.

## 8. Parameters

| Parameter | Default | Location | Adjustable |
|-----------|---------|----------|------------|
| `FeeParams.min_gas_price` | `10ngonka` | On-chain, `x/inference` params | Governance proposal |
| Bypass gas cap | `1,000,000` | `NetworkDutyFeeBypassDecorator` | Chain upgrade |
| Bypass priority | `500,000` | `NetworkDutyFeeBypassDecorator` | Chain upgrade |
| Fee-exempt message set | See Section 3.2 | `isExemptMessageType()` | Chain upgrade |
| DAPI gas price | `10ngonka` | `cosmosclient.go`, `tx_manager.go` | Config change |

## 9. Impact Assessment

### 9.1. End Users (Inference Consumers)

Negligible. Inference already requires escrow of `(max_tokens + prompt_tokens) × per_token_price`. At `10ngonka` gas price, the transaction fee (~800,000 ngonka, ~$0.00046) is comparable to a minimum inference escrow and immaterial relative to real-world inference costs.

### 9.2. Node Operators

*   **No impact on protocol duties.** PoC submissions, validations, BLS messages, and weight distributions are fee-exempt.
*   **Small fee on reward claims.** `MsgClaimRewards` requires a fee, but epoch rewards (285,000 GNK per epoch distributed by weight) far exceed it.
*   **Small fee on collateral operations.** Deposit/withdraw operations have a small fee, preventing deposit/withdraw cycling.

### 9.3. Validators

*   **New fee revenue.** Validators earn a share of collected fees via `x/distribution`, creating direct incentive to maintain network health.
*   **No configuration change needed.** Fee enforcement is consensus-level, not per-validator `app.toml`.

### 9.4. DAPI

*   **Mostly fee-exempt.** The batch system primarily submits network-duty messages covered by the bypass decorator.
*   **Feegrant option.** For remaining fee-required messages, feegrant allowances can cover operational costs.

### 9.5. Attackers

At `10ngonka` minimum gas price and ~80,000 gas per transaction, each spam transaction costs ~800,000 ngonka (~$0.00046). Flooding 10,000 transactions costs ~$4.56; 100,000 transactions costs ~$45.58. Sustained attacks become economically prohibitive, with fees deducted from the attacker and distributed to validators.

## 10. Files Modified

| File | Change |
|------|--------|
| `inference-chain/proto/inference/inference/params.proto` | Add `FeeParams` message with `min_gas_price` field |
| `inference-chain/x/inference/keeper/params.go` | Add `GetMinGasPrice()` accessor |
| `inference-chain/app/ante.go` | Add `NetworkDutyFeeBypassDecorator`, implement `NewGonkaFeeChecker`, wire into ante chain |
| `decentralized-api/cosmosclient/cosmosclient.go` | Set gas price to `10ngonka`, remove `WithFees` |
| `decentralized-api/cosmosclient/tx_manager/tx_manager.go` | Set gas price to `10ngonka`, fix batch gas limits |
