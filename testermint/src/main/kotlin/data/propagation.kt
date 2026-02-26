package com.productscience.data

import com.google.gson.annotations.SerializedName

/**
 * Bundle header propagated off-chain between participants
 */
data class PropagationBundleHeader(
    @SerializedName("bundle_id")
    val bundleId: String,
    val participant: String,
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
 * Info about when and what count was first seen for a participant
 */
data class ArrivalInfo(
    val time: Long,
    val count: Long
)

/**
 * Response from GET /v1/propagation/first-arrivals/{poc_height}
 */
data class PropagationFirstArrivalsResponse(
    @SerializedName("poc_height")
    val pocHeight: Long,
    val arrivals: Map<String, ArrivalInfo>
)

data class AgreedCountResponse(
    @SerializedName("agreed_count")
    val agreedCount: Long,
    val found: Boolean
)
