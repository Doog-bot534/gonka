package com.productscience.data

import com.google.gson.annotations.SerializedName

/**
 * Bundle header propagated off-chain between participants
 */
data class PropagationBundleHeader(
    @SerializedName("bundle_id")
    val bundleId: String,
    val participant: String,
    @SerializedName("pub_key")
    val pubKey: String,
    @SerializedName("poc_height")
    val pocHeight: Long,
    @SerializedName("root_hash")
    val rootHash: String,
    val count: Long,
    @SerializedName("created_at")
    val createdAt: Long,
    val signature: String
)

/**
 * Response from GET /v1/propagation/cache/{poc_height}
 */
data class PropagationCacheResponse(
    @SerializedName("poc_height")
    val pocHeight: Long,
    val count: Int
)

/**
 * Proof item for a single artifact in a bundle
 */
data class PropagationProofItem(
    @SerializedName("leaf_index")
    val leafIndex: Long,
    @SerializedName("nonce_value")
    val nonceValue: Int,
    @SerializedName("vector_bytes")
    val vectorBytes: String,
    val proof: List<String>
)

/**
 * Response from GET /v1/propagation/proofs/{bundle_id}
 */
data class PropagationProofsResponse(
    @SerializedName("bundle_id")
    val bundleId: String,
    val proofs: List<PropagationProofItem>
)
