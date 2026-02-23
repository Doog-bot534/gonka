# Inference Scaling 

## Problem 

Now per each inference next transactions are recorded on-chain:
- MsgStartInference
- MsgFinishInference
- MsgValidation (it can be from 0 to N_hosts transaction, current document consider 1 per inference)

3 txs per inference. Max capacity per block is ~5000
=> 5000 / 3 = 1666 inferences per block 
=> 1666 / 6 = 277 inferences per sec

Let's consider 4xH100 with deployed Qwen235B.
For 5000/1000 input/output tokens, such model can process 3.5-4 RPs (TODO: confirm)
=> 277 / 3.5 * 4 = 316 H100 GPU

Probably different requests can be backed together in single transaction.
Even if achieve 100x optimization with such approach, handling per request on chain billing is not scalable to hunderds thousands 

The situation becomes better with longer requests (more compute used, less billing info per unit of compute)
And much worse with smaller model (which are required for some domains)



> Note: in practice, the main limit is not even amount of transactions in blocks but even the computation per these 3 transaction.
Seems like it's becoming a problem now if there are more then couple hunderds such transactions per block. 
Based on profiling data it can be optimized significantly (2-10 times), but this limitation will still hit us first


## Proposal

This proposal describe approach which moves all per-inference communication off-chain. 
Chain will process only initial transaction to put coins in escrow and assign subgroup of hosts which will handle execute inferences. 
All communication around inferences and their validations will happen inside the subgroup directly during quite long period of time (e.g. epoch).
Then, user (or some of hosts) would have to settle the escrow, submitting final state of usage for the user signed by majority of hosts in such subgroup.
After such transaction submitted, user get's what left from escrow, participants are getting paid. 

Effectively, as each subgroup would have to achieve consensus for the final state, the architecture will consist of:
- main blockchain
- many sub-chains / shards with extremely lightweight architecture 

Sub-chains will be able to process only the inference related transactions and their decision might affect only the escrows, assigned to such sub-chains

### User Flow

- [mainchain]: user creates `MsgCreateEscrow(100GNK)` 
- [subchain]: user interact with hosts in subgroup in pre-defined order
- [mainnet]: at the end of session, user creates `MsgSettleEscrow(finalState, signatures, missed, invalid)`

Q1: How validation / invalidation stats from subchain is settled? Same `MsgSettleEscrow` or smth else. I'd consider separate to settle only decisions / punishments (they might be included also?)
Q2: Do Hosts have per group state, not per participant? 

A: Seems like it's better to start with decisions on main chain, to avoid global (not per used) state at each Host. But that can be decision inside the subgroup later

The further proposal will follow this lightweight architecture: "chain per user"


### Main Network Protocol

```
MsgCreateEscrow(
  creatorAddr
  amount,
)
```
1. move money to escrow via `MsgCreateEscrow`
2. return id to sample N(64?) slots-hosts (same design as optimize.md) 
3. interact in sub-chain during session 
4. settle on-chain via `MsgSettleEscrow`

```
MsgSettleEscrow(
  creatorAddr, # can be both user or someone from group
  finalState, # hash
  signatures, # signed by majority form group
  missed, # [groupMember -> uint32]
  invalid # [groupMember -> uint32]
)
```

5. On the escrow settlement, mainnet verifyes signatures from subnet. Must be signed by majoriry / supermajority
Once signatures are verified it settle escrow for the user, updated stats for hosts (missed, invalid)


### Sub-Chain Protocol

Let's focus on the subnet logic. Essentially, it's some sort of shard and might be considered as own blockchain with voting weight if fully provided by mainnet and then it's settled on mainnet when "session" is finished.

To make it more lightweight, parallizable and enforce user to use all hosts from the group for inferece requests, we introduce new assumption "per user state".

What user wants to do?
Send openapi-compartible REST API requests like `/chat/completions`, `/embeddings`, etc. And now as less as possible about the blockchain 

What chain wants (and essentiallly what we tried to achive on mainnet but less successfull that we would want to have):
- chain knows when request is started and finished => another hosts can measure performance of request to compare with expected performance and punish if some executor works much worse then expected (essentailly missed rate now)
- chain knows the initial hash of prompt and hash of final payload with signatures of user (for hash of prompt) and executor (for hash of payload). signatures of prompt is used to authorize payment, signature of payload if used for probabilistic inference validation (if invalid => invalidation rate punishment)
- chain enforces distribution of requests between executors proportionally to their weight

And as already mention - chain doesn't want to have all these data to be processed on mainnet

----

Key points of the idea:

- data is saved per user independely. for example, there is a chain of base diffs for the specific user / its transaction.state is updated based on such state 
  => it's possible to parallize processors by user. e.g. load balancer routes request from the user with address A to group of nodes which has access to DB with it's data. single state is not needed even inside the shard

- it's mainly responsibility of the user to propagate transaction (both created by the user itself and initiated from nodes at response)

- user attach diffs data in inference requests it makes 
  - gossiping might still be needed as it requires full round to propagate transaction. how to effectively propagate is open question

- signature of state(hieight) should be returned immediately after request
  - if `/chat/completions` request is sent host1, it essentially user creates `MsgStartInference(1)` task at host1. if host1 is honest, it must immediatly return `(state, signature)`, without waiting for execution of request. it then will return sign `MsgFinishInference(1)` and user will propagate it to the network (when it'll be in state of that user).

- user must iterate hosts in group by pre-defined order. it naturally distribute requests accross hosts (not the amount of real work but at least requests). It attach nonce to each transaction (or each diff) => it can't skip
  - if some host if not available, user keeps to propagate diffs to the next host. as it's essentially propagates ALL diffs in the current round (to cover all hosts), it'll attach diff without signature too. another host will follow protocol to decide together if host must be punished or not in that case
  - ideally, locks must be only on nonces producing and new messages from the host itself. we can't rely in waiting for response + signature from hosts => it might be additional optimistic delay in getting data. if to wait for at least some request result => it's wont allow to send requests fast



```
/chat/completions -d '{
  "model":"Qwen/Qwen3-235B-A22B-Instruct-2507-FP8",
  "stream":true, "logprobs":true, "top_logprobs": 5,
  "messages":[
    {"role":"system","content":"You are a helpful assistant."},
    {"role":"user","content":"Write a haiku about Seattle."}
  ],
  "diffs": [
    {
      "txs": [MsgStartInference(1)], # sent with first /chat/completions
      "signatures": [sign1, sign2, sign], # 
    },
    {
      "txs": [MsgStartInference(2)],  # sent with second /chat/completions
    },
    {
      "txs": [MsgStartInference(3), MsgFinishInference(2), MsgFinishInference(1)], # sent with third /chat/completions
    },
    
    
    
    ...
  ],
  "last_state_hash": "<SHA256>"
}'
```

Q1: how exactly to propagate signatures? each diff essentially a new block and has it's own signatures
Q2: currently consider that ever


### Weights in sub-chain


-----

InferenceGroup:
