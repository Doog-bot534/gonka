# MLNode PoC v2 Integration

This document describes the integration of vLLM PoC v2 (artifact-based proof-of-compute) into MLNode API.

## Overview

MLNode acts as a proxy and orchestration layer between the blockchain (API node) and multiple vLLM instances. The PoC v2 integration enables:

1. **Fan-out generation** - Single API call starts artifact generation across all vLLM backends with automatic `group_id` assignment
2. **Status-aware load balancing** - Validation requests routed to idle backends when possible
3. **Multi-backend aggregation** - Status and stop commands fan out to all backends

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                           API Node (Blockchain)                          │
│                                                                          │
│   POST /init/generate    POST /generate     GET /status    POST /stop   │
└─────────────────┬────────────────┬──────────────┬────────────┬──────────┘
                  │                │              │            │
                  ▼                ▼              ▼            ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                              MLNode API                                  │
│                         (packages/api)                                   │
│                                                                          │
│  ┌─────────────────────────────────────────────────────────────────────┐│
│  │                    /api/v1/inference/pow/*                          ││
│  │                                                                      ││
│  │  /init/generate  →  Fan-out to all backends with group_id injection ││
│  │  /generate       →  Route to idle backend, composite request_id     ││
│  │  /status         →  Aggregate from all backends                     ││
│  │  /stop           →  Fan-out stop to all backends                    ││
│  └─────────────────────────────────────────────────────────────────────┘│
│                                                                          │
│  ┌─────────────────────────────────────────────────────────────────────┐│
│  │                         Proxy Layer                                  ││
│  │                                                                      ││
│  │  - vllm_backend_ports: [5001, 5002, ...]                            ││
│  │  - vllm_healthy: {port: bool}                                       ││
│  │  - poc_status_by_port: {port: "IDLE"|"GENERATING"|"STOPPED"}        ││
│  │  - pick_backend_for_pow_generate() → prefers non-GENERATING         ││
│  └─────────────────────────────────────────────────────────────────────┘│
└─────────────────┬────────────────┬──────────────────────────────────────┘
                  │                │
         ┌────────┴────────┐ ┌────┴────────┐
         ▼                 ▼ ▼             ▼
┌─────────────────┐ ┌─────────────────┐ ┌─────────────────┐
│   vLLM :5001    │ │   vLLM :5002    │ │   vLLM :500N    │
│   group_id=0    │ │   group_id=1    │ │   group_id=N-1  │
│                 │ │                 │ │                 │
│ /api/v1/pow/*   │ │ /api/v1/pow/*   │ │ /api/v1/pow/*   │
└─────────────────┘ └─────────────────┘ └─────────────────┘
         │                 │                   │
         └─────────────────┴───────────────────┘
                           │
                           ▼
                  ┌─────────────────┐
                  │  Batch Receiver │
                  │   (Callbacks)   │
                  │                 │
                  │ POST /generated │
                  │ POST /validated │
                  └─────────────────┘
```

## New Files

### Source Code

| File | Description |
|------|-------------|
| `src/api/inference/pow_v2_routes.py` | PoC v2 route handlers with fan-out and LB logic |
| `src/api/proxy.py` | Extended with `poc_status_by_port` and status-aware picker |

### Tests

| File | Description |
|------|-------------|
| `tests/unit/test_pow_v2_routes.py` | Mock-based unit tests for routing logic |
| `tests/integration/test_pow_v2_e2e.py` | Full E2E tests with real GPU |
| `tests/batch_receiver_v2.py` | Batch receiver for artifact-based callbacks |

## API Endpoints

All endpoints under `/api/v1/inference/pow/` (works when vLLM/inference is running).

### POST /api/v1/inference/pow/init/generate

Start artifact generation on all healthy vLLM backends.

**Request:**
```json
{
  "block_hash": "0xabc123...",
  "block_height": 12345,
  "public_key": "node_pubkey",
  "node_id": 0,
  "node_count": 10,
  "batch_size": 32,
  "params": {
    "model": "Qwen/Qwen3-0.6B",
    "seq_len": 256,
    "k_dim": 12
  },
  "url": "http://callback-receiver:8080"
}
```

**Behavior:**
- MLNode calls each healthy backend with injected `group_id=i` and `n_groups=N`
- Each backend generates unique nonce stream: `offset = node_id + group_id * n_nodes`
- Callbacks sent to `{url}/generated` with artifact batches

**Response:**
```json
{
  "status": "OK",
  "backends": 2,
  "n_groups": 2,
  "results": [{"port": 5001, "status": "OK"}, {"port": 5002, "status": "OK"}],
  "errors": null
}
```

### POST /api/v1/inference/pow/generate

Compute artifacts for specific nonces, optionally validate against provided artifacts.

**Request (with validation):**
```json
{
  "block_hash": "0xabc123...",
  "block_height": 12345,
  "public_key": "node_pubkey",
  "node_id": 0,
  "node_count": 10,
  "nonces": [100, 101, 102, ...],
  "params": {"model": "...", "seq_len": 256, "k_dim": 12},
  "batch_size": 32,
  "wait": true,
  "validation": {
    "artifacts": [
      {"nonce": 100, "vector_b64": "base64..."},
      {"nonce": 101, "vector_b64": "base64..."}
    ]
  },
  "stat_test": {
    "dist_threshold": 0.02,
    "p_mismatch": 0.001,
    "fraud_threshold": 0.01
  }
}
```

**Behavior:**
- Routes to backend preferring IDLE over GENERATING (status-aware LB)
- For `wait=false`, returns composite `request_id` as `{port}:{backend_uuid}`

**Response (validation):**
```json
{
  "status": "completed",
  "request_id": "5001:abc123-uuid",
  "n_total": 50,
  "n_mismatch": 0,
  "mismatch_nonces": [],
  "p_value": 1.0,
  "fraud_detected": false
}
```

### GET /api/v1/inference/pow/generate/{request_id}

Poll for result of queued `/generate` request.

**Behavior:**
- Parses composite `request_id` to extract port and backend UUID
- Routes poll to correct backend

### GET /api/v1/inference/pow/status

Aggregate status from all backends.

**Response:**
```json
{
  "status": "GENERATING",  // or "IDLE", "MIXED", "NO_BACKENDS"
  "backends": [
    {"port": 5001, "status": "GENERATING", "stats": {"total_processed": 1000}},
    {"port": 5002, "status": "IDLE"}
  ]
}
```

### POST /api/v1/inference/pow/stop

Stop generation on all backends.

**Response:**
```json
{
  "status": "OK",
  "results": [{"port": 5001, "status": "stopped"}, {"port": 5002, "status": "stopped"}],
  "errors": null
}
```

## Proxy Layer Extensions

### PoC Status Tracking

```python
# In api/proxy.py
poc_status_by_port: Dict[int, str] = {}  # "IDLE", "GENERATING", "STOPPED", ""

# Updated in health check loop
async def _health_check_vllm(interval: float = 2.0):
    for p in vllm_backend_ports:
        # ... existing health check ...
        if ok:
            r = await vllm_client.get(f"http://{VLLM_HOST}:{p}/api/v1/pow/status")
            poc_status_by_port[p] = r.json().get("status", "")
```

### Status-Aware Backend Selection

```python
async def pick_backend_for_pow_generate() -> int:
    """Pick backend for /generate, preferring non-GENERATING."""
    async with vllm_pick_lock:
        live = [p for p, ok in vllm_healthy.items() if ok]
        
        # Prefer backends not actively generating
        non_generating = [p for p in live if poc_status_by_port.get(p) != "GENERATING"]
        candidates = non_generating if non_generating else live
        
        # Least-connections among candidates
        port = min(candidates, key=lambda p: vllm_counts.get(p, 0))
        vllm_counts[port] += 1
        return port
```

## Testing

### Unit Tests (Mock-based)

Location: `tests/unit/test_pow_v2_routes.py`

| Test Class | Tests |
|------------|-------|
| `TestInitGenerateFanout` | Fan-out with group_id injection, no-backends handling |
| `TestValidationLBPrefersIdle` | Prefers IDLE backend, falls back when all GENERATING |
| `TestQueuedRequestIdRoundtrip` | Composite request_id creation and polling |
| `TestStopFanout` | Stop calls all backends |
| `TestStatusAggregation` | Status aggregation (MIXED, GENERATING) |

Run: `pytest tests/unit/test_pow_v2_routes.py -v`

### E2E Integration Tests

Location: `tests/integration/test_pow_v2_e2e.py`

#### TestPoCv2E2E - Happy Path

| Test | Description |
|------|-------------|
| `test_full_flow` | Full E2E: deploy, generate, validate artifacts, stop |
| `test_status_reflects_state` | Status transitions IDLE → GENERATING → STOPPED |

**test_full_flow steps:**
1. Clear batch receiver, stop services
2. Deploy `Qwen/Qwen3-0.6B` with `--enable-poc`
3. Wait for vLLM ready
4. `POST /init/generate` with callback URL
5. Wait for artifacts (min 10)
6. `POST /stop`
7. Parse artifacts: decode base64 → fp16 → validate 12 dims, no NaN/Inf
8. `POST /generate` with validation (subset of artifacts)
9. Assert `n_mismatch=0`, `fraud_detected=false`
10. Verify no callbacks after stop
11. Cleanup

#### TestPoCv2FraudDetection - Negative Tests

| Test | Scenario | Expected |
|------|----------|----------|
| `test_fraud_wrong_pubkey` | Same artifacts, different `public_key` | `fraud_detected=true` |
| `test_fraud_wrong_nonces` | Artifacts with mismatched nonces | `400 Bad Request` |
| `test_fraud_modified_vectors` | 20% vectors corrupted (L2 >> 0.02) | `n_mismatch >= 10`, `fraud_detected=true` |
| `test_small_perturbation_passes` | All vectors perturbed (L2 ~1e-3) | `n_mismatch=0`, `fraud_detected=false` |

### Running Integration Tests

```bash
cd packages/api
make integration-tests
```

Or with docker-compose:

```bash
docker compose down --remove-orphans
docker compose run --rm integration-tests pytest -v /app/packages/api/tests/integration/test_pow_v2_e2e.py
```

### Batch Receiver v2

Location: `tests/batch_receiver_v2.py`

Endpoints:
- `POST /generated` - receives artifact batches
- `POST /validated` - receives validation results
- `GET /generated` - returns received batches
- `GET /validated` - returns validation results
- `POST /clear` - clears all data
- `GET /health` - health check

Docker service: `batch-receiver-v2` on port 8080

## Artifact Encoding

Vectors are base64-encoded little-endian float16:

```python
import base64
import numpy as np

def decode_artifact_vector(vector_b64: str, k_dim: int = 12) -> np.ndarray:
    """Decode base64 fp16 little-endian vector."""
    data = base64.b64decode(vector_b64)
    vec = np.frombuffer(data, dtype='<f2')  # little-endian float16
    return vec.astype(np.float32)

def encode_vector(vec: np.ndarray) -> str:
    """Encode numpy vector to base64 fp16."""
    f16 = vec.astype(np.float16)
    return base64.b64encode(f16.tobytes()).decode('ascii')
```

**Size:** 12 dims × 2 bytes = 24 bytes raw → ~32 bytes base64

## Validation Logic

### Distance Threshold
- `dist_threshold = 0.02` - L2 distance above which vectors are mismatched
- Small hardware variance (~1e-3) passes; large differences (>0.02) fail

### Statistical Fraud Test
```python
from scipy.stats import binomtest

def fraud_test(n_mismatch, n_total, p_mismatch=0.001, fraud_threshold=0.01):
    result = binomtest(n_mismatch, n_total, p_mismatch, alternative='greater')
    fraud_detected = result.pvalue < fraud_threshold
    return result.pvalue, fraud_detected
```

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `BATCH_RECEIVER_V2_URL` | - | URL for artifact callbacks (integration tests) |
| `SERVER_URL` | - | MLNode API URL (integration tests) |

### Docker Compose Services

```yaml
services:
  server:        # MLNode API with GPU
  batch-receiver-v2:  # Artifact callback receiver
  integration-tests:  # Test runner
```

## Compatibility

- **v1 PoW routes unchanged** - `/api/v1/pow/*` continues to work (legacy distance-based)
- **OpenAI proxy unchanged** - `/v1/*` proxied to vLLM backends
- **No conflict with inference mode** - PoC v2 routes work when vLLM is running (no service conflict check)
