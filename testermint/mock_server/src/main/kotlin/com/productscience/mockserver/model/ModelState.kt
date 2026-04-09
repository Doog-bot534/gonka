package com.productscience.mockserver.model

import org.slf4j.LoggerFactory
import java.util.concurrent.atomic.AtomicLong

/**
 * Enum representing the possible states of the model.
 */
enum class ModelState {
    POW,
    INFERENCE,
    TRAIN,
    STOPPED
}

enum class PowState {
    POW_IDLE,
    POW_NO_CONTROLLER,
    POW_LOADING,
    POW_GENERATING,
    POW_VALIDATING,
    POW_STOPPED,
    POW_MIXED
}
private val powLogger = LoggerFactory.getLogger("PowState")
private val modelLogger = LoggerFactory.getLogger("ModelState")
fun getModelState(host: String): ModelState {
    val orDefault = modelStates.getOrDefault(host, ModelState.STOPPED)
    modelLogger.debug("Model state for host: $host is: $orDefault")
    return orDefault
}

fun setModelState(host: String, state: ModelState) {
    modelLogger.debug("Setting model state for host: $host to: $state")
    modelStates[host] = state
}

fun getPowState(host: String): PowState {
    val orDefault = powStates.getOrDefault(host, PowState.POW_STOPPED)
    powLogger.debug("POW state for host: $host is: $orDefault")
    return orDefault
}

fun setPowState(host: String, state: PowState) {
    powLogger.debug("Setting POW state for host: $host to: $state")
    powStates[host] = state
}

val latestNonce = AtomicLong(1) // legacy, kept for V1 compat

// Per-model nonce counters for V2 PoC. Each model gets its own counter
// so artifacts from one model don't inflate another model's nonces,
// which would trip the porosity check (maxNonce / count < 100).
private val modelNonces = java.util.concurrent.ConcurrentHashMap<String, AtomicLong>()

fun getModelNonce(modelId: String): AtomicLong =
    modelNonces.computeIfAbsent(modelId) { AtomicLong(0) }
val modelStates = mutableMapOf<String, ModelState>()
val powStates = mutableMapOf<String, PowState>()