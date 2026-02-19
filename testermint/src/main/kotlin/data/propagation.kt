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

data class PoCObservationArrivalEntry(
    val participant: String,
    val count: Long
)

data class PoCObservationEntry(
    @SerializedName("validator_address")
    val validatorAddress: String,
    @SerializedName("poc_stage_start_block_height")
    val pocStageStartBlockHeight: Long,
    val arrivals: List<PoCObservationArrivalEntry>,
    @SerializedName("block_height")
    val blockHeight: Long
)

data class PoCObservationsResponse(
    val observations: List<PoCObservationEntry>
)

data class PoCConsensusEntryData(
    val participant: String,
    @SerializedName("agreed_count")
    val agreedCount: Long,
    @SerializedName("total_validators")
    val totalValidators: Int,
    @SerializedName("agreeing_count")
    val agreeingCount: Int
)

data class PoCConsensusResponse(
    val entries: List<PoCConsensusEntryData>
)
