# New PoC Offchain Step-by-Step Testing and Migration

1. **Version 2.0.1:** We are creating a version of the `mlnode` and `api` nodes so that they function exactly as before but also include additional functionality and endpoints   
   1. As we want to iterate through multiple versions with a potentially different set of participants, all apis should be versioned so only triggered for nodes with the latest version (e.g. through adding to all requests `X-API-Version: 2.0.1`).  
      1. **Version discovery**: use the existing `GET /v1/versions` endpoint for discovery, but add a **new dedicated field** in its JSON response for this protocol (e.g. `poc_api_version` ), rather than reusing existing `api_version.version` fields that may already be used for other purposes.  
   2. **PoCStart Endpoint**: Used to launch a new "poc." It records the "poc" using the same storage backends we already support for inference payloads (**file storage and/or Postgres**, not SQLite). If records for an old poc are already being made, the new records must be completely separate to avoid confusion. It also sends a request to the `mlnode` to start generating new "nonces" for the next 60 seconds (duration), sending results every 5 seconds (frequency).
      1. The endpoint accepts requests with an `X-TA-Signature` only from a specific hardcoded public key; this is a temporary endpoint that will be removed in production.  
      2. If a new PoC cannot start due to an old PoC or is interrupted by an old PoC, we record the interruption time in `interrupted_time`. On start, we perform **two independent checks**:
         1. **Legacy/v1 PoC (broker-controlled) active**: if the existing PoC flow is currently running (as reflected in broker node PoC statuses), then the v2 PoC is considered interrupted immediately.
         2. **Prior v2 PoC run (storage-controlled) active**: if the latest stored v2 run is still active (not finished and not interrupted), we mark that previous v2 run as interrupted and also consider this new v2 PoC interrupted immediately.  
      3. api POST v2/poc/start:  
         1. `block_height` (int64)  
         2. `epoch_length` (int64) \- for the future, initially unused  
         3. `block_hash` (string)  
         4. `block_time` (time.Time)  
         5. `duration` (int64)  
         6. `frequency` (int64)  
         7. `batch_size` (int)  
         8. `params` (object; PoC model params)  
            1. `model` (string)  
            2. `seq_len` (int)  
            3. `k_dim` (int; default 12)  
      4. Storage (same backends as inference payload storage):  
         1. Storage backends should mirror `decentralized-api/payloadstorage/*` (notably `payloadstorage/file_storage.go` and `payloadstorage/postgres_storage.go`, optionally `payloadstorage/hybrid_storage.go` and `payloadstorage/managed_storage.go`).  
         2. Postgres should use the same connection mechanism as payload storage (standard libpq env vars already used by `payloadstorage/postgres_storage.go`: PGHOST/PGPORT/PGDATABASE/PGUSER/PGPASSWORD).  
         3. The PoC run record fields (regardless of backend): `block_height` (key), `epoch_length` (future), `block_hash`, `block_time`, `duration`, `frequency`, `interrupted_time`.  
      5. ML node trigger (generation start request):  
         1. The API node triggers each ML node generation via the ML node HTTP endpoint `POST /init/generate` (router `@router.post("/init/generate")`).  
         2. Request body (JSON) `PoCInitGenerateRequest`:  
            1. `block_hash` (string)  
            2. `block_height` (int)  
            3. `public_key` (string)  
            4. `node_id` (int)  
            5. `node_count` (int)  
            6. `group_id` (int; default 0)  
            7. `n_groups` (int; default 1)  
            8. `batch_size` (int; default POC_BATCH_SIZE_DEFAULT)  
            9. `params` (object; `PoCParamsModel`)  
               1. `model` (string)  
               2. `seq_len` (int)  
               3. `k_dim` (int; default 12)  
            10. `url` (string; optional)  
   3. **PoCArtifactsGenerated Endpoint**: For the new **artifact-based** format received from `mlnode`. When the API node receives artifacts, it saves a record of the result (results may be received multiple times; each result is saved as a separate record): `node_id` of the mlnode (node num → broker node id string), current count of artifacts for this "poc," hash of all artifacts received so far, time since `block_time`, and an array of artifacts.  
      1. api POST /v2/poc-artifacts/generated:  
         1. `public_key` (string)  
         2. `block_hash` (string)  
         3. `block_height` (int64)  
         4. `node_id` (int)  
         5. `artifacts` (\[\]ArtifactV2)  
            1. `nonce` (int64)  
            2. `vector_b64` (string; base64-encoded fp16 little-endian vector)  
         6. `encoding` (object; optional; informational; ignored by decentralized-api)  
         7. `request_id` (string; optional)  
      2. Storage: Uses the same storage backends as inference payloads (file storage and/or Postgres), though nonces may eventually be stored with the payload.  
         1. `block_height`  
         2. `address` (participant own address)  
         3. `node_id` (string)  
         4. `model` (string)  
         5. `amount`  
         6. `hash`  
         7. `time_since_block`  
         8. `artifacts` (\[\]ArtifactV2) (internal storage representation)
            1. `nonce` (int64)
            2. `vector_b64` (string)
   4. **PoCResult Endpoint:** Allows viewing PoC results for each participant (currently only one), each of their ML nodes, and each stored emission record (at 5, 10, etc. second intervals): time since `block_time`, artifact count (`amount`), rolling `hash`, and the **artifacts** themselves (with `vector_b64`).  
      1. The endpoint returns results for the last launched PoC, but a `GET` parameter can be used to view results for a different `block_height` (if there is no PoC with that block\_height, the endpoint should return the closest known PoC which happens before that block\_height)    
      2. The endpoint accepts requests with an X-TA-Signature only from a specific hardcoded public key; this is a temporary endpoint that will be removed in production.   
      3. api GET v2/poc/results?block\_height=...
         1. poc  
            1. block\_height  
            2. … (other parameters of the poc)  
            3. participants (now only one participant, but on the next stages we will have many)  
               1. address  
               2. results  
                  1. nodes  
                     1. node\_id  
                        1. model (string)  
                        2. results \[\] (each stored emission record)  
                           1. amount  
                           2. hash  
                           3. time\_since\_block  
                           4. artifacts \[\]  
                              1. nonce (int64)  
                              2. vector\_b64 (string)  
2. **Testing 2.0.1:** DevOps is asked to run this version to check API availability, results, and how it correlates with participant weight  
3. **Version 2.0.2:** If everything goes well, we add more functionality  
   1. The PoCResult endpoint can accept “results” from other participants in the form of counts and hashes (without the artifacts themselves), and write them to the database (the result will be received many times) together with the time of receipt since block\_time and with the sender’s signature  
      1. POST /v2/poc/results (the API accepts requests only from X-TA-Signature active validators, verifies against the address, accepts only for the latest known block\_height poc)  
         1. block\_height  
         2. address  
         3. nodes  
            1. node\_id  
            2. model  
            3. amount (artifact count)  
            4. hash (rolling hash over artifacts)  
      2. we should make the endpoint in such a way that through a single POST connection it is possible to receive many sequential results for different ml nodes and different time\_since  
      3. storage in db  
         1. block\_height  
         2. address  
         3. node\_id  
         4. model  
         5. block\_height  
         6. time\_since (own time, since block\_time)  
         7. amount  
         8. hash  
   2. the API, receiving artifacts from mlnode, sends to all other active participants, in parallel to all at the same time, the result summary of its work (amount/hash), and records the “work received” signature in the record of its own work result for this node  
      1. in the beginning of PoC we make one POST connection for each participant (which will last 5 seconds longer than duration of the PoC), and keep it alive and sending results through it (batching in a 100 ms window)  
         1. **FD limit requirement**: because this implies potentially thousands of simultaneous outbound peer connections, the API process must check its current FD limit (RLIMIT_NOFILE), and raise its **soft** limit (to 5x participants) at startup before opening peer connections. Also add to /v1/versions the effective limit for debugging.
   3. In the PoCResult endpoint, we add results from other participants who sent their results (how many artifacts, hashes, and when they sent them)  
      1. GET /v2/poc/results  
4. **Testing 2.0.2:** We ask DevOps to launch this version, and we check if all nodes have received the same number of results, and what the delay was.  
5. **Version 2.0.3:** If successful, we create a version that, after starting, includes a mechanism that itself launches nonce generation every X blocks, based on the hash and time of that block.  
   1. The PoC launch is moved to a reaction to a block (we use the block hash and `block_time`).  
   2. The `PoCStart` endpoint only determines which block to start from (`block_height`), and how often to launch (`epoch_length`).  
   3. We add another API that shows a vector for each participant of how much weight they had after "delay" seconds in different PoCs starting from "blockHeight".  
      1. `GET v2/poc/stats?delay=...\&blockHeight=...`  
   4. And another `PoCStart` endpoint that allows stopping the procedure (the API accepts a request only from a specific public key).  
      1. `POST v2/poc/stop`  
6. **Testing 2.0.3:** We ask DevOps to launch this version, and we check if all nodes are consistently participating, if anyone drops out of participation (does their weight drop), how quickly we get a consistent stable result (after 15, 30, 45, 60 seconds).  
7. **Version 2.0.4:** If successful, we perform validation.  
   1. PoCResult Enpoint POST should validate that the model for node id for participant is correct one (think about other missing validations)  
   2. PoCNonces Endpoint, new endpoint allow to request N full nonces with particular sequential number  
      1. GET /v1/poc/nonces?blockHeight=...\&key=node:seq&...\&key=node:seq  
         1. nodes  
            1. node\_id  
            2. nonces \[\]  
   3. When the duration of the PoC ends, for each participant who sent PoC results, api node should simultaneously requests 200 random nonces (from any of their mlnodes with models supported by this participant), receives the nonce, and kick starts their verification, when the verification finished, records the overall verification result for each participant.  
      1. storage in db  
         1. `block_height`  
         2. `address`  
         3. `vote`   
         4. `interapted_time`  
   4. We add a vote to the PoC Result for each participant, add the flag `?vote_only=true`.  
8. **Testing 2.0.4:** We ask DevOps to launch this version, and we check who has the available API and what they get, and how this will correlate with their weight.
