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

/**
 * Observation of first arrival times from a validator
 */
data class FirstArrivalObservation(
    @SerializedName("validator_address")
    val validatorAddress: String,
    @SerializedName("poc_height")
    val pocHeight: Long,
    val arrivals: Map<String, ArrivalInfo>,
    val timestamp: Long,
    val signature: String
)

/**
 * Response from GET /v1/propagation/observations/{poc_height}
 */
data class PropagationObservationsResponse(
    @SerializedName("poc_height")
    val pocHeight: Long,
    val observations: List<FirstArrivalObservation>
)

/**
 * Consensus result for a single participant
 */
data class ConsensusResult(
    @SerializedName("poc_height")
    val pocHeight: Long,
    val participant: String,
    @SerializedName("agreed_count")
    val agreedCount: Long,
    @SerializedName("total_validators")
    val totalValidators: Int,
    @SerializedName("agreeing_count")
    val agreeingCount: Int
)

/**
 * Response from GET /v1/propagation/consensus/{poc_height}
 */
data class PropagationConsensusResponse(
    @SerializedName("poc_height")
    val pocHeight: Long,
    val deadline: Long,
    val consensus: Map<String, ConsensusResult>
)
