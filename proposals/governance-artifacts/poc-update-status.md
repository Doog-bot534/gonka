# PoC Update Status

This document describes the current status of the PoC v2 migration, which integrates the PoC procedure into vLLM and uses `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8` model.

## Summary

The PoC v2 initiative addresses two key objectives:

1. Integrate the PoC procedure directly into vLLM, enabling an immediate switch from inference to PoC without offloading the model or loading a separate PoC model.
2. Migrate artifact storage off-chain using Merkle commitments to reduce on-chain data volume.

These changes maintain robustness against minimal model changes (e.g., quantization differences). In current vLLM integration, inference and PoC are not co-executed within the same forward pass, but an incoming chat completion can be processed in the next forward pass (<100ms at 4xH100). A potential next step is to integrate PoC into the `/completion` engine so inference and PoC can run in the same batch and use vLLM dynamic batching.

---

## vLLM Integration

A working prototype is available at: https://github.com/gonka-ai/vllm/tree/gm/poc-layers

Instead of offloading the inference model and loading a randomly initialized model for PoC, the integrated approach applies block-dependent randomization to specific layers:

- Input: bypass token embeddings by feeding deterministic random `inputs_embeds` seeded by `(block_hash, public_key, nonce)`.
- Hidden layers: apply per-layer Householder transforms via forward hooks seeded by `(block_hash, layer_idx)`, active only during PoC forward context.
- Output: from the last-token hidden state, normalize, select `k_dim` indices per nonce, and apply a deterministic Haar-like rotation (Householder chain) seeded by `(block_hash, public_key, nonce)`.
- Artifact: output is a `k_dim` vector (default `k_dim=12`, FP16, base64-encoded).

The produced artifacts can reliably distinguish models with minimal differences (e.g., FP8 vs INT4, FP8 vs W8A16). Analysis for `Qwen/Qwen3-235B-A22B-Instruct-2507` (FP8 vs W8A16) across A100, H100, H200, and B200 hardware is available at:
https://github.com/gonka-ai/vllm/blob/gm/poc-layers/scripts/analyze_data_poc2_235-instruct_layers.ipynb

The implementation is functional but requires additional testing before deployment.

---

## Off-Chain Data Architecture

PoC v2 artifacts require more data per artifact than v1. To maintain an adequate ratio between PoC generation time and PoC validation time, the total number of generated nonces per node must be high enough to support meaningful sampling. Storing full artifact batches on-chain creates scalability constraints, and we have already observed issues with too many transactions being recorded.

Proposed approach: store only a commitment on-chain (Merkle root + leaf count), while keeping full artifacts off-chain. Validators query the chain for the committed `(root_hash, count)`, sample leaf indices, request artifacts and proofs from participants over an API, and verify inclusion before statistical validation.

Prototype of the off-chain is implemented. It requires additional testing and refinements before deployment.

---

## Migration Strategy

For a smooth transition from PoC v1 to PoC v2, the chain must ensure that the majority of participants have switched to the new vLLM build and support the `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8` model before PoC v2 becomes the main PoC engine.

The idea we consider is to use PoC v2 first only for the Confirmation PoC, while PoC v1 remains the engine used for the main PoC stage. Once adoption is sufficient (measured by the total weight and/or the number of PoC v2 validations reaching the majority threshold), the system will automatically promote PoC v2 to be the main PoC engine.
