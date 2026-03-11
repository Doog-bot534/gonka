## Summary
Create a new module, bookkeeper, that encapsulates a simple wrapper around `bank` that logs transactions in a double-entry accounting style (and a "simple" style more suitable for examining in test logs).
Introduce the idea of sub-account transactions, to keep track of balance changes on things like coins-owed, collateral unbonding, etc.
Sub-accounts are essentially modeled as claims-registries. They are **tracking only** and are not an actual Cosmos account.

### Implementation status
- **Module**: `inference-chain/x/bookkeeper/`; proto under `inference-chain/proto/inference/bookkeeper/`.
- **Wrapper**: Keeper implements `BookkeepingBankKeeper` (SendCoins, MintCoins, BurnCoins, etc.): delegates to `bank` then logs. Other modules (inference, collateral, streamvesting) use this keeper for both transfers and/or `LogSubAccountTransaction` only.
- **Sub-accounts**: Used with names like `settled`, `owed` (inference), `collateral`, `collateral-unbonding` (collateral), `vesting` (streamvesting). In logs, accounts are formatted as `{owner}_{subAccount}` (e.g. `bob_settled`, `inference_settled`).
### Example
For example, rewards vesting flow is as follows:
- Mint Rewards: This shows as moving from `supply` into `inference`.
- Settled Sub-account: The reward amount for an account (let's say for `bob`) is "moved" from `bob-settled` to `inference-settled`, showing the `inference-settled` owes bob that amount (when they claim).
- Claim comes in. Three things happen (just rewards, let's ignore work):
1. Actual account movement: `inference` moves the reward amount to `streamvesting`
2. Vesting sub-account: `bob-vesting` moves the reward amount to `streamvesting-vesting` (to mark the amount owed to `bob`
3. Claim Settled: `inference-settled` moves the reward amount to `bob-settled`, zeroing out the `bob-settled` amount.

- Later, when a vesting portion happens:
1. Actual account movement: `streamvesting` moves the vested portion to `bob`
2. Vesting sub-account: `streamvesting-vesting` moves the vested portion to `bob-vesting` (reducing the bob-vesting amount)

- In the event of a forfeited claim:
1. Actual account movement: `inference` moves the reward amount to `supply` (burning)
2. Settled sub-account: `inference-settled` moves the reward amount to `inference-bob` (the account has been settled)

### Future Possible Work
1. Currently `bookkeeper` only logs. It could store a proper ledger on-chain, or in a database.
2. `bookkeeper` should emit events.
3. `bookkeeper` could model balances for sub-accounts, allowing a more detailed audit and status for these accounts. This _might_ even alleviate the need to have entries like vesting_owed, though things like vesting period would still need to be stored.
4. **Done (partially):** Log level and double/simple entry are not in chain Params; they are set per node via module `LogConfig` (injected in keeper constructor; in this repo, set in `app/app_config.go`). Making them configurable at node runtime (e.g. config file) is still a TODO in app_config.
