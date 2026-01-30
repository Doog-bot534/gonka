package com.productscience.data

import com.google.gson.annotations.SerializedName

/**
 * Bundle header propagated off-chain between participants
 */
data class PropagationBundleHeader(
    @SerializedName("bundle_id")
    val bundleId: String,  // hex-encoded 32 bytes
    val participant: String,
    @SerializedName("pub_key")
    val pubKey: String,  // hex-encoded public key
    @SerializedName("poc_height")
    val pocHeight: Long,
    @SerializedName("root_hash")
    val rootHash: String,  // hex-encoded
    val count: Long,
    @SerializedName("created_at")
    val createdAt: Long,  // unix timestamp
    val signature: String  // hex-encoded
)

/**
 * Message sent via HTTP to propagate header to children in tree
 */
data class PropagationHeaderMessage(
    @SerializedName("tree_idx")
    val treeIdx: Int,
    val header: PropagationBundleHeader,
    val from: String? = null  // Optional: defaults to header.participant
)

/**
 * Response from GET /v1/propagation/cache/{poc_height}
 * Returns all cached commit metadata for a given PoC height
 */
data class PropagationCacheResponse(
    @SerializedName("poc_height")
    val pocHeight: Long,
    val bundles: List<PropagationBundleHeader>
)

/**
 * Statistics about propagation system
 */
data class PropagationStatsResponse(
    @SerializedName("total_bundles")
    val totalBundles: Int,
    @SerializedName("total_participants")
    val totalParticipants: Int,
    @SerializedName("cache_size_bytes")
    val cacheSizeBytes: Long
)
