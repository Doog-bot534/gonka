package com.productscience

import com.google.gson.annotations.SerializedName
import com.productscience.data.MsgCreateEscrow
import com.productscience.data.OpenAIResponse
import com.productscience.data.TxResponse
import org.bitcoinj.core.ECKey
import org.bitcoinj.core.Sha256Hash
import org.bouncycastle.asn1.ASN1Integer
import org.bouncycastle.asn1.ASN1Primitive
import org.bouncycastle.asn1.ASN1Sequence
import org.bouncycastle.asn1.sec.SECNamedCurves
import org.bouncycastle.crypto.params.ECDomainParameters
import org.bouncycastle.crypto.params.ECPublicKeyParameters
import org.bouncycastle.crypto.signers.ECDSASigner
import org.tinylog.kotlin.Logger
import java.io.ByteArrayOutputStream
import java.math.BigInteger
import java.net.HttpURLConnection
import java.net.SocketTimeoutException
import java.nio.ByteBuffer
import java.security.MessageDigest
import java.time.Clock
import java.util.Base64
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit
import java.util.concurrent.locks.ReentrantLock
import kotlin.concurrent.withLock

interface InferenceV2Client {
    fun createEscrow(request: CreateEscrowRequest = CreateEscrowRequest()): EscrowSessionClient
    fun attachEscrow(context: V2EscrowContext): EscrowSessionClient
}

interface EscrowSessionClient {
    fun complete(
        request: InferenceRequestPayload,
        options: RequestOptions = RequestOptions(),
    ): ClientOutcome<ClientResult<OpenAIResponse>>

    fun stream(
        request: InferenceRequestPayload,
        options: RequestOptions = RequestOptions(),
    ): ClientOutcome<ClientStreamResult>

    fun retry(
        handle: RetryHandle,
        options: RetryOptions = RetryOptions(),
    ): ClientOutcome<RetryResult>

    fun snapshot(): EscrowClientSnapshot
}

interface TestableEscrowSessionClient : EscrowSessionClient {
    fun reserveHandleForTesting(request: InferenceRequestPayload): RetryHandle
    fun retryForTesting(
        handle: RetryHandle,
        options: RetryOptions = RetryOptions(),
        recordFinishOnSuccess: Boolean = true,
    ): ClientOutcome<RetryResult>
    fun retryHandleForSequence(sequence: Long): RetryHandle
    fun blockJsonForSequence(sequence: Long): String
    fun retryWithModifiedEnvelopeForTesting(
        handle: RetryHandle,
        options: RetryOptions = RetryOptions(),
        resignMode: EnvelopeResignMode = EnvelopeResignMode.NONE,
        recordFinishOnSuccess: Boolean = true,
        mutateEnvelope: (com.google.gson.JsonObject) -> Unit,
    ): ClientOutcome<RetryResult>
    fun recordFinishForTesting(
        handle: RetryHandle,
        openAiResponse: OpenAIResponse? = null,
        responsePayloadHash: String,
        executorAddress: String?,
        executorSignerAddress: String?,
        executorSignerPubKey: String?,
        executorSignature: String?,
        status: String = "finished",
    )
}

data class CreateEscrowRequest(
    val modelId: String = defaultModel,
)

data class InferenceV2ClientConfig(
    val genesis: LocalInferencePair,
    val allPairs: List<LocalInferencePair>,
    val developerAddress: String,
    val developerBlockSigner: V2DeveloperBlockSigner,
    val pairByAddress: Map<String, LocalInferencePair>,
    val weightedParticipants: List<V2WeightedParticipant>,
    val responsibleParticipantCount: Int = 3,
    val clock: Clock = Clock.systemUTC(),
)

data class V2EscrowContext(
    val escrowId: String,
    val epochId: Long,
    val modelId: String,
)

data class V2WeightedParticipant(
    val address: String,
    val weight: ULong,
)

data class V2DeveloperBlockSigner(
    val signingAccountAddress: String,
    val chainId: String,
    val signPayloadHex: (String) -> String,
)

data class RequestOptions(
    val timeout: java.time.Duration? = null,
    val leaderTimeout: java.time.Duration? = null,
    val preferredInitialRecipient: String? = null,
    val sendToAllResponsible: Boolean = false,
    val allowFanoutOnTimeout: Boolean = true,
    val includeDiagnostics: Boolean = false,
)

data class RetryOptions(
    val preferredInitialRecipient: String? = null,
    val resendToAllResponsible: Boolean = false,
    val includeDiagnostics: Boolean = false,
)

sealed interface ClientOutcome<out T>

data class ClientSuccess<T>(
    val result: T,
) : ClientOutcome<T>

data class ClientFailure(
    val error: ClientError,
    val retryHandle: RetryHandle? = null,
    val relayArtifacts: List<V2RelayErrorArtifact> = emptyList(),
    val missedInferenceQueued: Boolean = false,
    val attempts: List<AttemptTrace> = emptyList(),
) : ClientOutcome<Nothing>

data class ClientError(
    val code: String,
    val message: String,
    val retryable: Boolean = false,
    val cause: Throwable? = null,
)

enum class AttemptStatus {
    SUCCESS,
    FAILURE,
}

data class AttemptTrace(
    val recipientAddress: String,
    val status: AttemptStatus,
    val latestBlockSequence: Long? = null,
    val responseId: String? = null,
    val errorMessage: String? = null,
)

data class RetryHandle(
    val escrowId: String,
    val sequence: Long,
    val requestId: String,
    val responsibleParticipants: List<String>,
    val requestPayloadHash: String,
    val stream: Boolean,
    val requestStateRef: String,
)

sealed interface RetryResult

data class RetryCompletion(
    val result: ClientResult<OpenAIResponse>,
) : RetryResult

data class RetryStream(
    val result: ClientStreamResult,
) : RetryResult

data class ClientResult<T>(
    val value: T,
    val requestId: String,
    val sequence: Long,
    val escrowId: String,
    val latestBlockSequence: Long,
    val recipientAddress: String,
    val executorAddress: String?,
    val executorSignerAddress: String? = null,
    val executorSignerPubKey: String? = null,
    val executorSignature: String? = null,
    val responsePayloadHash: String? = null,
    val responsibleParticipants: List<String>,
    val attempts: List<AttemptTrace> = emptyList(),
)

data class ClientStreamResult(
    val requestId: String,
    val sequence: Long,
    val escrowId: String,
    val latestBlockSequence: Long,
    val recipientAddress: String,
    val responsibleParticipants: List<String>,
    val retryHandle: RetryHandle,
    val stream: LineReadableStream,
    val attempts: List<AttemptTrace> = emptyList(),
)

data class EscrowClientSnapshot(
    val context: V2EscrowContext,
    val latestReservedSequence: Long,
    val chainTipSequence: Long,
    val acknowledgedByRecipient: Map<String, Long>,
    val reservedRequestRefs: Set<String>,
)

data class V2ExecutorProof(
    @SerializedName("executor_address")
    val executorAddress: String,
    @SerializedName("executor_signer_address")
    val executorSignerAddress: String,
    @SerializedName("executor_signer_pubkey")
    val executorSignerPubKey: String,
    @SerializedName("executor_signature")
    val executorSignature: String,
)

data class V2RelayErrorArtifact(
    @SerializedName("escrow_id")
    val escrowId: String,
    @SerializedName("request_id")
    val requestId: String,
    @SerializedName("intended_executor_address")
    val intendedExecutorAddress: String,
    @SerializedName("relay_address")
    val relayAddress: String,
    @SerializedName("failure_code")
    val failureCode: String,
    @SerializedName("relay_signer_address")
    val relaySignerAddress: String,
    @SerializedName("relay_signer_pubkey")
    val relaySignerPubKey: String,
    @SerializedName("relay_signature")
    val relaySignature: String,
    @SerializedName("timestamp")
    val timestamp: Long,
)

enum class EnvelopeResignMode {
    NONE,
    RECOMPUTE_STATE_AND_SIGN,
    SIGN_USING_EXISTING_STATE_HASH,
}

class TestermintInferenceV2Client(
    private val config: InferenceV2ClientConfig,
) : InferenceV2Client {
    override fun createEscrow(request: CreateEscrowRequest): EscrowSessionClient {
        val txResponse = config.genesis.submitMessage(
            MsgCreateEscrow(
                creator = config.developerAddress,
                modelId = request.modelId,
            )
        )
        require(txResponse.code == 0) { "Create escrow tx failed with code=${txResponse.code}" }
        val context = requireNotNull(extractEscrowContext(txResponse, request.modelId)) {
            "Could not extract escrow context from tx events: ${txResponse.events}"
        }
        val indexedHeightBarrier = txResponse.height + 1
        config.allPairs.forEach { pair ->
            pair.node.waitForMinimumBlock(indexedHeightBarrier, "escrow indexing barrier")
        }
        return attachEscrow(context)
    }

    override fun attachEscrow(context: V2EscrowContext): EscrowSessionClient {
        return TestermintEscrowSessionClient(config = config, context = context)
    }

    private fun extractEscrowContext(txResponse: TxResponse, modelId: String): V2EscrowContext? {
        val event = txResponse.events.firstOrNull { it.type == "escrow_created" } ?: return null
        val escrowId = event.attributes.firstOrNull { it.key == "escrow_id" || it.key.endsWith(".escrow_id") }?.value
            ?: return null
        val epochId = event.attributes.firstOrNull { it.key == "epoch_id" || it.key.endsWith(".epoch_id") }?.value
            ?.toLongOrNull()
            ?: return null
        return V2EscrowContext(
            escrowId = escrowId,
            epochId = epochId,
            modelId = modelId,
        )
    }
}

private class TestermintEscrowSessionClient(
    private val config: InferenceV2ClientConfig,
    private val context: V2EscrowContext,
) : TestableEscrowSessionClient {
    private val lock = ReentrantLock()
    private var latestReservedSequence = 0L
    private val chainBlocks = mutableListOf<DeveloperChainBlock>()
    private var deterministicState = DeterministicChainState()
    private val pendingFinishMessagesByRequestSequence = linkedMapOf<Long, DeveloperChainMessage>()
    private val pendingMissedMessagesByRequestSequence = linkedMapOf<Long, DeveloperChainMessage>()
    private val acknowledgedByRecipient = mutableMapOf<String, Long>()
    private val reservedRequestsByRef = linkedMapOf<String, ReservedRequestState>()

    override fun complete(
        request: InferenceRequestPayload,
        options: RequestOptions,
    ): ClientOutcome<ClientResult<OpenAIResponse>> {
        if (request.stream) {
            return ClientFailure(
                error = ClientError(
                    code = "STREAM_REQUEST_REQUIRES_STREAM_API",
                    message = "Use stream() for streaming requests",
                    retryable = false,
                ),
            )
        }
        val reserved = reserveRequest(request)
        if (options.sendToAllResponsible) {
            return dispatchNonStreamingFanout(reserved, buildRetryHandle(reserved))
        }

        val recipientAddress = resolveInitialRecipient(reserved, options.preferredInitialRecipient)
            ?: return invalidRecipientFailure(reserved, options.preferredInitialRecipient)
        val attempt = dispatchNonStreamingAttempt(
            reserved = reserved,
            recipientAddress = recipientAddress,
            readTimeoutMs = resolveLeaderTimeoutMs(options),
        )
        if (attempt is DispatchAttempt.Success) {
            return buildCompletionSuccess(
                reserved = reserved,
                chosenAttempt = attempt,
                successfulAttempts = listOf(attempt),
                failedAttempts = emptyList(),
            )
        }
        val failure = attempt as DispatchAttempt.Failure
        val retryHandle = buildRetryHandle(reserved)
        if (options.allowFanoutOnTimeout && isRetryableTransportFailure(failure.cause)) {
            return dispatchNonStreamingFanout(reserved, retryHandle, seedFailures = listOf(failure))
        }
        return ClientFailure(
            error = clientError(
                code = "INITIAL_REQUEST_FAILED",
                cause = failure.cause,
                retryable = true,
            ),
            retryHandle = retryHandle,
            relayArtifacts = failure.relayArtifact?.let(::listOf).orEmpty(),
            attempts = listOf(failure.trace),
        )
    }

    override fun stream(
        request: InferenceRequestPayload,
        options: RequestOptions,
    ): ClientOutcome<ClientStreamResult> {
        val reserved = reserveRequest(request.copy(stream = true))
        val retryHandle = buildRetryHandle(reserved)
        if (options.sendToAllResponsible) {
            return ClientFailure(
                error = ClientError(
                    code = "STREAM_FANOUT_UNSUPPORTED",
                    message = "Streaming fanout is not implemented yet",
                    retryable = true,
                ),
                retryHandle = retryHandle,
            )
        }
        val recipientAddress = resolveInitialRecipient(reserved, options.preferredInitialRecipient)
            ?: return invalidRecipientFailure(reserved, options.preferredInitialRecipient)
        val envelopeJson = buildEnvelopeJson(reserved, recipientAddress)
        return try {
            val pair = requireNotNull(config.pairByAddress[recipientAddress]) {
                "Missing pair for recipient=$recipientAddress"
            }
            val connection = createV2StreamConnection(
                url = "${pair.api.getPublicUrl()}/v2/chat/completions",
                requesterAddress = config.developerAddress,
                escrowId = context.escrowId,
                sequence = reserved.sequence,
                epochId = context.epochId,
                jsonBody = envelopeJson,
            )
            recordReceiverAcknowledgment(recipientAddress, connection.latestBlockSequence)
            val managedStream = ManagedV2ClientStream(
                rawStream = connection.streamConnection,
                onCompleted = { completion ->
                    recordFinishInference(
                        requestSequence = reserved.sequence,
                        openAiResponse = null,
                        responsePayloadHash = completion.responsePayloadHash,
                        executorAddress = completion.executorProof.executorAddress,
                        executorSignerAddress = completion.executorProof.executorSignerAddress,
                        executorSignerPubKey = completion.executorProof.executorSignerPubKey,
                        executorSignature = completion.executorProof.executorSignature,
                        inputTokenCountOverride = completion.inputTokenCount,
                        outputTokenCountOverride = completion.outputTokenCount,
                    )
                },
            )
            ClientSuccess(
                ClientStreamResult(
                    requestId = reserved.requestId,
                    sequence = reserved.sequence,
                    escrowId = context.escrowId,
                    latestBlockSequence = connection.latestBlockSequence,
                    recipientAddress = recipientAddress,
                    responsibleParticipants = reserved.responsibleParticipants,
                    retryHandle = retryHandle,
                    stream = managedStream,
                    attempts = listOf(
                        AttemptTrace(
                            recipientAddress = recipientAddress,
                            status = AttemptStatus.SUCCESS,
                            latestBlockSequence = connection.latestBlockSequence,
                        )
                    ),
                )
            )
        } catch (cause: Exception) {
            ClientFailure(
                error = clientError(
                    code = "STREAM_REQUEST_FAILED",
                    cause = cause,
                    retryable = true,
                ),
                retryHandle = retryHandle,
                attempts = listOf(
                    AttemptTrace(
                        recipientAddress = recipientAddress,
                        status = AttemptStatus.FAILURE,
                        errorMessage = cause.message ?: cause::class.simpleName.orEmpty(),
                    )
                ),
            )
        }
    }

    override fun retry(
        handle: RetryHandle,
        options: RetryOptions,
    ): ClientOutcome<RetryResult> {
        return retryInternal(handle, options, recordFinishOnSuccess = true)
    }

    override fun retryForTesting(
        handle: RetryHandle,
        options: RetryOptions,
        recordFinishOnSuccess: Boolean,
    ): ClientOutcome<RetryResult> {
        return retryInternal(handle, options, recordFinishOnSuccess = recordFinishOnSuccess)
    }

    private fun retryInternal(
        handle: RetryHandle,
        options: RetryOptions,
        recordFinishOnSuccess: Boolean,
    ): ClientOutcome<RetryResult> {
        val reserved = lock.withLock { reservedRequestsByRef[handle.requestStateRef] }
            ?: return ClientFailure(
                error = ClientError(
                    code = "UNKNOWN_RETRY_HANDLE",
                    message = "No reserved request state found for ${handle.requestStateRef}",
                    retryable = false,
                ),
            )

        if (reserved.sequence != handle.sequence || reserved.requestId != handle.requestId) {
            return ClientFailure(
                error = ClientError(
                    code = "STALE_RETRY_HANDLE",
                    message = "Retry handle does not match reserved request state",
                    retryable = false,
                ),
            )
        }

        return if (reserved.openAiRequest.stream) {
            retryStream(reserved, handle, options)
        } else {
            retryCompletion(reserved, handle, options, recordFinishOnSuccess)
        }
    }

    override fun snapshot(): EscrowClientSnapshot = lock.withLock {
        EscrowClientSnapshot(
            context = context,
            latestReservedSequence = latestReservedSequence,
            chainTipSequence = chainBlocks.lastOrNull()?.blockSequence ?: 0L,
            acknowledgedByRecipient = acknowledgedByRecipient.toMap(),
            reservedRequestRefs = reservedRequestsByRef.keys.toSet(),
        )
    }

    override fun retryHandleForSequence(sequence: Long): RetryHandle = lock.withLock {
        val reserved = reservedRequestsByRef.values.firstOrNull { it.sequence == sequence }
            ?: error("No reserved request found for sequence=$sequence")
        buildRetryHandle(reserved)
    }

    override fun reserveHandleForTesting(request: InferenceRequestPayload): RetryHandle {
        return buildRetryHandle(reserveRequest(request))
    }

    override fun blockJsonForSequence(sequence: Long): String = lock.withLock {
        val block = chainBlocks.firstOrNull { it.blockSequence == sequence }
            ?: error("No block found for sequence=$sequence")
        cosmosJson.toJson(block)
    }

    override fun retryWithModifiedEnvelopeForTesting(
        handle: RetryHandle,
        options: RetryOptions,
        resignMode: EnvelopeResignMode,
        recordFinishOnSuccess: Boolean,
        mutateEnvelope: (com.google.gson.JsonObject) -> Unit,
    ): ClientOutcome<RetryResult> {
        val reserved = lock.withLock { reservedRequestsByRef[handle.requestStateRef] }
            ?: return ClientFailure(
                error = ClientError(
                    code = "UNKNOWN_RETRY_HANDLE",
                    message = "No reserved request state found for ${handle.requestStateRef}",
                    retryable = false,
                ),
            )
        val recipientAddress = resolveInitialRecipient(reserved, options.preferredInitialRecipient)
            ?: return invalidRecipientFailure(reserved, options.preferredInitialRecipient)
        val envelopeJson = buildEnvelopeJson(reserved, recipientAddress)
        val envelopeTree = cosmosJson.fromJson(envelopeJson, com.google.gson.JsonObject::class.java)
        mutateEnvelope(envelopeTree)
        val mutatedEnvelopeJson = if (resignMode != EnvelopeResignMode.NONE) {
            val envelope = cosmosJson.fromJson(cosmosJson.toJson(envelopeTree), DeveloperChainEnvelope::class.java)
            cosmosJson.toJson(resignEnvelopeForTesting(envelope, resignMode))
        } else {
            cosmosJson.toJson(envelopeTree)
        }

        return if (reserved.openAiRequest.stream) {
            val retryOutcome = dispatchModifiedStreamForTesting(
                reserved = reserved,
                handle = handle,
                recipientAddress = recipientAddress,
                envelopeJson = mutatedEnvelopeJson,
            )
            when (retryOutcome) {
                is ClientSuccess -> ClientSuccess(RetryStream(retryOutcome.result))
                is ClientFailure -> retryOutcome
            }
        } else {
            val attempt = dispatchNonStreamingAttempt(
                reserved = reserved,
                recipientAddress = recipientAddress,
                readTimeoutMs = null,
                envelopeJson = mutatedEnvelopeJson,
            )
            when (attempt) {
                is DispatchAttempt.Success -> {
                    when (val outcome = buildCompletionSuccess(
                        reserved = reserved,
                        chosenAttempt = attempt,
                        successfulAttempts = listOf(attempt),
                        failedAttempts = emptyList(),
                        recordFinishOnSuccess = recordFinishOnSuccess,
                    )) {
                        is ClientSuccess -> ClientSuccess(RetryCompletion(outcome.result))
                        is ClientFailure -> outcome
                    }
                }
                is DispatchAttempt.Failure -> ClientFailure(
                    error = clientError(
                        code = "MODIFIED_RETRY_FAILED",
                        cause = attempt.cause,
                        retryable = false,
                    ),
                    retryHandle = handle,
                    relayArtifacts = attempt.relayArtifact?.let(::listOf).orEmpty(),
                    attempts = listOf(attempt.trace),
                )
            }
        }
    }

    override fun recordFinishForTesting(
        handle: RetryHandle,
        openAiResponse: OpenAIResponse?,
        responsePayloadHash: String,
        executorAddress: String?,
        executorSignerAddress: String?,
        executorSignerPubKey: String?,
        executorSignature: String?,
        status: String,
    ) {
        val reserved = lock.withLock { reservedRequestsByRef[handle.requestStateRef] }
            ?: error("No reserved request state found for ${handle.requestStateRef}")
        recordFinishInference(
            requestSequence = reserved.sequence,
            openAiResponse = openAiResponse,
            responsePayloadHash = responsePayloadHash,
            executorAddress = executorAddress,
            executorSignerAddress = executorSignerAddress,
            executorSignerPubKey = executorSignerPubKey,
            executorSignature = executorSignature,
            status = status,
        )
    }

    private fun retryCompletion(
        reserved: ReservedRequestState,
        handle: RetryHandle,
        options: RetryOptions,
        recordFinishOnSuccess: Boolean,
    ): ClientOutcome<RetryResult> {
        if (options.resendToAllResponsible) {
            return when (val outcome = dispatchNonStreamingFanout(reserved, handle, recordFinishOnSuccess = recordFinishOnSuccess)) {
                is ClientSuccess -> ClientSuccess(RetryCompletion(outcome.result))
                is ClientFailure -> outcome
            }
        }
        val recipientAddress = resolveInitialRecipient(reserved, options.preferredInitialRecipient)
            ?: return invalidRecipientFailure(reserved, options.preferredInitialRecipient)
        val attempt = dispatchNonStreamingAttempt(
            reserved = reserved,
            recipientAddress = recipientAddress,
            readTimeoutMs = null,
        )
        return when (attempt) {
            is DispatchAttempt.Success -> {
                when (val outcome = buildCompletionSuccess(
                    reserved = reserved,
                    chosenAttempt = attempt,
                    successfulAttempts = listOf(attempt),
                    failedAttempts = emptyList(),
                    recordFinishOnSuccess = recordFinishOnSuccess,
                )) {
                    is ClientSuccess -> ClientSuccess(RetryCompletion(outcome.result))
                    is ClientFailure -> outcome
                }
            }
            is DispatchAttempt.Failure -> ClientFailure(
                error = clientError(
                    code = "RETRY_FAILED",
                    cause = attempt.cause,
                    retryable = true,
                ),
                retryHandle = handle,
                relayArtifacts = attempt.relayArtifact?.let(::listOf).orEmpty(),
                attempts = listOf(attempt.trace),
            )
        }
    }

    private fun retryStream(
        reserved: ReservedRequestState,
        handle: RetryHandle,
        options: RetryOptions,
    ): ClientOutcome<RetryResult> {
        if (options.resendToAllResponsible) {
            return ClientFailure(
                error = ClientError(
                    code = "STREAM_FANOUT_UNSUPPORTED",
                    message = "Streaming fanout is not implemented yet",
                    retryable = true,
                ),
                retryHandle = handle,
            )
        }
        val recipientAddress = resolveInitialRecipient(reserved, options.preferredInitialRecipient)
            ?: return invalidRecipientFailure(reserved, options.preferredInitialRecipient)
        val envelopeJson = buildEnvelopeJson(reserved, recipientAddress)
        return try {
            val pair = requireNotNull(config.pairByAddress[recipientAddress]) {
                "Missing pair for recipient=$recipientAddress"
            }
            val connection = createV2StreamConnection(
                url = "${pair.api.getPublicUrl()}/v2/chat/completions",
                requesterAddress = config.developerAddress,
                escrowId = context.escrowId,
                sequence = reserved.sequence,
                epochId = context.epochId,
                jsonBody = envelopeJson,
            )
            recordReceiverAcknowledgment(recipientAddress, connection.latestBlockSequence)
            val managedStream = ManagedV2ClientStream(
                rawStream = connection.streamConnection,
                onCompleted = { completion ->
                    recordFinishInference(
                        requestSequence = reserved.sequence,
                        openAiResponse = null,
                        responsePayloadHash = completion.responsePayloadHash,
                        executorAddress = completion.executorProof.executorAddress,
                        executorSignerAddress = completion.executorProof.executorSignerAddress,
                        executorSignerPubKey = completion.executorProof.executorSignerPubKey,
                        executorSignature = completion.executorProof.executorSignature,
                        inputTokenCountOverride = completion.inputTokenCount,
                        outputTokenCountOverride = completion.outputTokenCount,
                    )
                },
            )
            ClientSuccess(
                RetryStream(
                    ClientStreamResult(
                        requestId = reserved.requestId,
                        sequence = reserved.sequence,
                        escrowId = context.escrowId,
                        latestBlockSequence = connection.latestBlockSequence,
                        recipientAddress = recipientAddress,
                        responsibleParticipants = reserved.responsibleParticipants,
                        retryHandle = handle,
                        stream = managedStream,
                        attempts = listOf(
                            AttemptTrace(
                                recipientAddress = recipientAddress,
                                status = AttemptStatus.SUCCESS,
                                latestBlockSequence = connection.latestBlockSequence,
                            )
                        ),
                    )
                )
            )
        } catch (cause: Exception) {
            ClientFailure(
                error = clientError(
                    code = "STREAM_RETRY_FAILED",
                    cause = cause,
                    retryable = true,
                ),
                retryHandle = handle,
                attempts = listOf(
                    AttemptTrace(
                        recipientAddress = recipientAddress,
                        status = AttemptStatus.FAILURE,
                        errorMessage = cause.message ?: cause::class.simpleName.orEmpty(),
                    )
                ),
            )
        }
    }

    private fun dispatchModifiedStreamForTesting(
        reserved: ReservedRequestState,
        handle: RetryHandle,
        recipientAddress: String,
        envelopeJson: String,
    ): ClientOutcome<ClientStreamResult> {
        return try {
            val pair = requireNotNull(config.pairByAddress[recipientAddress]) {
                "Missing pair for recipient=$recipientAddress"
            }
            val connection = createV2StreamConnection(
                url = "${pair.api.getPublicUrl()}/v2/chat/completions",
                requesterAddress = config.developerAddress,
                escrowId = context.escrowId,
                sequence = reserved.sequence,
                epochId = context.epochId,
                jsonBody = envelopeJson,
            )
            recordReceiverAcknowledgment(recipientAddress, connection.latestBlockSequence)
            val managedStream = ManagedV2ClientStream(
                rawStream = connection.streamConnection,
                onCompleted = { completion ->
                    recordFinishInference(
                        requestSequence = reserved.sequence,
                        openAiResponse = null,
                        responsePayloadHash = completion.responsePayloadHash,
                        executorAddress = completion.executorProof.executorAddress,
                        executorSignerAddress = completion.executorProof.executorSignerAddress,
                        executorSignerPubKey = completion.executorProof.executorSignerPubKey,
                        executorSignature = completion.executorProof.executorSignature,
                    )
                },
            )
            ClientSuccess(
                ClientStreamResult(
                    requestId = reserved.requestId,
                    sequence = reserved.sequence,
                    escrowId = context.escrowId,
                    latestBlockSequence = connection.latestBlockSequence,
                    recipientAddress = recipientAddress,
                    responsibleParticipants = reserved.responsibleParticipants,
                    retryHandle = handle,
                    stream = managedStream,
                    attempts = listOf(
                        AttemptTrace(
                            recipientAddress = recipientAddress,
                            status = AttemptStatus.SUCCESS,
                            latestBlockSequence = connection.latestBlockSequence,
                        )
                    ),
                )
            )
        } catch (cause: Exception) {
            ClientFailure(
                error = clientError(
                    code = "MODIFIED_STREAM_RETRY_FAILED",
                    cause = cause,
                    retryable = false,
                ),
                retryHandle = handle,
                attempts = listOf(
                    AttemptTrace(
                        recipientAddress = recipientAddress,
                        status = AttemptStatus.FAILURE,
                        errorMessage = cause.message ?: cause::class.simpleName.orEmpty(),
                    )
                ),
            )
        }
    }

    private fun resignEnvelopeForTesting(
        envelope: DeveloperChainEnvelope,
        resignMode: EnvelopeResignMode,
    ): DeveloperChainEnvelope = lock.withLock {
        var state = computeDeterministicStateUpToSequence(envelope.developerChainDelta.baseBlockSequence)
        val resignedBlocks = envelope.developerChainDelta.blocks.map { block ->
            val signed = when (resignMode) {
                EnvelopeResignMode.RECOMPUTE_STATE_AND_SIGN -> signDeveloperChainBlockWithState(
                    blockSequence = block.blockSequence,
                    escrowId = block.escrowId,
                    messages = block.messages,
                    developerBlockSigner = config.developerBlockSigner,
                    baseState = state,
                )
                EnvelopeResignMode.SIGN_USING_EXISTING_STATE_HASH -> signDeveloperChainBlockWithExplicitStateHash(
                    blockSequence = block.blockSequence,
                    escrowId = block.escrowId,
                    messages = block.messages,
                    developerBlockSigner = config.developerBlockSigner,
                    stateHash = block.stateHash,
                    nextState = applyDeterministicState(state, block.messages),
                )
                EnvelopeResignMode.NONE -> error("EnvelopeResignMode.NONE should not reach resignEnvelopeForTesting")
            }
            state = signed.nextState
            signed.block
        }
        envelope.copy(
            developerChainDelta = envelope.developerChainDelta.copy(
                blocks = resignedBlocks,
            )
        )
    }

    private fun reserveRequest(openAiRequest: InferenceRequestPayload): ReservedRequestState = lock.withLock {
        val sequence = latestReservedSequence + 1
        latestReservedSequence = sequence
        val requestId = buildRequestId(context.escrowId, sequence)
        val requestPayloadHash = computeRequestPayloadHash(openAiRequest)
        val responsibleParticipants = selectResponsibleParticipantsDeterministic(
            participants = config.weightedParticipants,
            selectionCount = config.responsibleParticipantCount,
            escrowId = context.escrowId,
            sequence = sequence,
        )

        val finishMessages = pendingFinishMessagesByRequestSequence.toSortedMap().values.toList()
        pendingFinishMessagesByRequestSequence.clear()
        val missedMessages = pendingMissedMessagesByRequestSequence.toSortedMap().values.toList()
        pendingMissedMessagesByRequestSequence.clear()

        val blockMessages = finishMessages + missedMessages + listOf(
            DeveloperChainMessage(
                type = START_INFERENCE_MESSAGE_TYPE,
                requestId = requestId,
                modelId = context.modelId,
                requestPayloadHash = requestPayloadHash,
                timestamp = nowSeconds(),
            )
        )
        val signedBlock = signDeveloperChainBlockWithState(
            blockSequence = sequence,
            escrowId = context.escrowId,
            messages = blockMessages,
            developerBlockSigner = config.developerBlockSigner,
            baseState = deterministicState,
        )
        deterministicState = signedBlock.nextState
        chainBlocks += signedBlock.block

        val reserved = ReservedRequestState(
            requestStateRef = requestId,
            requestId = requestId,
            sequence = sequence,
            requestPayloadHash = requestPayloadHash,
            openAiRequest = openAiRequest,
            responsibleParticipants = responsibleParticipants,
            intendedRecipientAddress = responsibleParticipants.first(),
        )
        reservedRequestsByRef[reserved.requestStateRef] = reserved
        reserved
    }

    private fun buildEnvelopeJson(
        reserved: ReservedRequestState,
        recipientAddress: String,
    ): String = lock.withLock {
        val recipientAcked = acknowledgedByRecipient[recipientAddress] ?: 0L
        val baseBlockSequence = minOf(recipientAcked, reserved.sequence - 1L)
        val deltaBlocks = chainBlocks.filter { block ->
            block.blockSequence > baseBlockSequence && block.blockSequence <= reserved.sequence
        }
        val envelope = DeveloperChainEnvelope(
            openAiRequest = reserved.openAiRequest,
            developerChainDelta = DeveloperChainDelta(
                baseBlockSequence = baseBlockSequence,
                blocks = deltaBlocks,
                latestBlockSequence = reserved.sequence,
            ),
        )
        cosmosJson.toJson(envelope)
    }

    private fun dispatchNonStreamingFanout(
        reserved: ReservedRequestState,
        retryHandle: RetryHandle,
        seedFailures: List<DispatchAttempt.Failure> = emptyList(),
        recordFinishOnSuccess: Boolean = true,
    ): ClientOutcome<ClientResult<OpenAIResponse>> {
        val recipients = reserved.responsibleParticipants
        val envelopes = recipients.associateWith { recipient -> buildEnvelopeJson(reserved, recipient) }
        val executor = Executors.newFixedThreadPool(recipients.size)
        val failures = seedFailures.toMutableList()
        try {
            val futures = recipients.map { recipientAddress ->
                executor.submit<DispatchAttempt> {
                    dispatchNonStreamingAttempt(
                        reserved = reserved,
                        recipientAddress = recipientAddress,
                        readTimeoutMs = null,
                        envelopeJson = envelopes.getValue(recipientAddress),
                    )
                }
            }
            val attempts = futures.map { future ->
                runCatching { future.get(90, TimeUnit.SECONDS) }.getOrElse { cause ->
                    DispatchAttempt.Failure(
                        trace = AttemptTrace(
                            recipientAddress = "unknown",
                            status = AttemptStatus.FAILURE,
                            errorMessage = cause.message ?: cause::class.simpleName.orEmpty(),
                        ),
                        cause = cause,
                    )
                }
            }
            val successes = attempts.filterIsInstance<DispatchAttempt.Success>()
            failures += attempts.filterIsInstance<DispatchAttempt.Failure>()
            if (successes.isEmpty()) {
                val relayArtifacts = failures.mapNotNull { it.relayArtifact }
                val missedInferenceQueued = maybeQueueMissedInference(reserved, relayArtifacts)
                return ClientFailure(
                    error = ClientError(
                        code = "FANOUT_FAILED",
                        message = failures.firstOrNull()?.cause?.message ?: "Fanout failed for all recipients",
                        retryable = true,
                        cause = failures.firstOrNull()?.cause,
                    ),
                    retryHandle = retryHandle,
                    relayArtifacts = relayArtifacts,
                    missedInferenceQueued = missedInferenceQueued,
                    attempts = failures.map { it.trace },
                )
            }
            val chosen = ensureConsistentFanoutResults(successes) ?: return ClientFailure(
                error = ClientError(
                    code = "INCONSISTENT_FANOUT_RESULT",
                    message = "Responsible participants returned inconsistent responses for ${reserved.requestId}",
                    retryable = false,
                ),
                retryHandle = retryHandle,
                attempts = (successes.map { it.trace } + failures.map { it.trace }),
            )
            return buildCompletionSuccess(
                reserved = reserved,
                chosenAttempt = chosen,
                successfulAttempts = successes,
                failedAttempts = failures,
                recordFinishOnSuccess = recordFinishOnSuccess,
            )
        } finally {
            executor.shutdownNow()
        }
    }

    private fun ensureConsistentFanoutResults(
        successes: List<DispatchAttempt.Success>,
    ): DispatchAttempt.Success? {
        val canonicalHash = successes.first().response.responsePayloadHash
        if (successes.any { it.response.responsePayloadHash != canonicalHash }) {
            Logger.warn("Fanout returned mismatched payload hashes for request={}", successes.first().reserved.requestId)
            return null
        }
        return successes.first()
    }

    private fun buildCompletionSuccess(
        reserved: ReservedRequestState,
        chosenAttempt: DispatchAttempt.Success,
        successfulAttempts: List<DispatchAttempt.Success>,
        failedAttempts: List<DispatchAttempt.Failure>,
        recordFinishOnSuccess: Boolean = true,
    ): ClientOutcome<ClientResult<OpenAIResponse>> {
        successfulAttempts.forEach { success ->
            recordReceiverAcknowledgment(success.recipientAddress, success.response.latestBlockSequence)
        }
        return try {
            if (recordFinishOnSuccess) {
                recordFinishInference(
                    requestSequence = reserved.sequence,
                    openAiResponse = chosenAttempt.response.openAIResponse,
                    responsePayloadHash = chosenAttempt.response.responsePayloadHash
                        ?: computeResponsePayloadHash(chosenAttempt.response.openAIResponse),
                    executorAddress = chosenAttempt.response.executorAddress,
                    executorSignerAddress = chosenAttempt.response.executorSignerAddress,
                    executorSignerPubKey = chosenAttempt.response.executorSignerPubKey,
                    executorSignature = chosenAttempt.response.executorSignature,
                )
            }
            ClientSuccess(
                ClientResult(
                    value = chosenAttempt.response.openAIResponse,
                    requestId = reserved.requestId,
                    sequence = reserved.sequence,
                    escrowId = context.escrowId,
                    latestBlockSequence = chosenAttempt.response.latestBlockSequence,
                    recipientAddress = chosenAttempt.recipientAddress,
                    executorAddress = chosenAttempt.response.executorAddress,
                    executorSignerAddress = chosenAttempt.response.executorSignerAddress,
                    executorSignerPubKey = chosenAttempt.response.executorSignerPubKey,
                    executorSignature = chosenAttempt.response.executorSignature,
                    responsePayloadHash = chosenAttempt.response.responsePayloadHash,
                    responsibleParticipants = reserved.responsibleParticipants,
                    attempts = (successfulAttempts.map { it.trace } + failedAttempts.map { it.trace }),
                )
            )
        } catch (cause: Exception) {
            ClientFailure(
                error = clientError(
                    code = "FINISH_RECORDING_FAILED",
                    cause = cause,
                    retryable = false,
                ),
                retryHandle = buildRetryHandle(reserved),
                attempts = (successfulAttempts.map { it.trace } + failedAttempts.map { it.trace }),
            )
        }
    }

    private fun computeDeterministicStateUpToSequence(sequence: Long): DeterministicChainState {
        var state = DeterministicChainState()
        chainBlocks
            .filter { it.blockSequence <= sequence }
            .sortedBy { it.blockSequence }
            .forEach { block ->
                state = applyDeterministicState(state, block.messages)
            }
        return state
    }

    private fun dispatchNonStreamingAttempt(
        reserved: ReservedRequestState,
        recipientAddress: String,
        readTimeoutMs: Int?,
        envelopeJson: String = buildEnvelopeJson(reserved, recipientAddress),
    ): DispatchAttempt {
        val pair = runCatching {
            requireNotNull(config.pairByAddress[recipientAddress]) { "Missing pair for recipient=$recipientAddress" }
        }.getOrElse { cause ->
            return DispatchAttempt.Failure(
                trace = AttemptTrace(
                    recipientAddress = recipientAddress,
                    status = AttemptStatus.FAILURE,
                    errorMessage = cause.message ?: cause::class.simpleName.orEmpty(),
                ),
                cause = cause,
                relayArtifact = null,
            )
        }
        return try {
            val response = makeV2Request(
                publicUrl = pair.api.getPublicUrl(),
                request = envelopeJson,
                requesterAddress = config.developerAddress,
                escrowId = context.escrowId,
                sequence = reserved.sequence,
                epochId = context.epochId,
                readTimeoutMs = readTimeoutMs,
            )
            DispatchAttempt.Success(
                reserved = reserved,
                recipientAddress = recipientAddress,
                response = response,
                trace = AttemptTrace(
                    recipientAddress = recipientAddress,
                    status = AttemptStatus.SUCCESS,
                    latestBlockSequence = response.latestBlockSequence,
                    responseId = response.openAIResponse.id,
                ),
            )
        } catch (cause: Exception) {
            DispatchAttempt.Failure(
                trace = AttemptTrace(
                    recipientAddress = recipientAddress,
                    status = AttemptStatus.FAILURE,
                    errorMessage = cause.message ?: cause::class.simpleName.orEmpty(),
                ),
                cause = cause,
                relayArtifact = (cause as? V2RequestHttpException)?.relayArtifact,
            )
        }
    }

    private fun makeV2Request(
        publicUrl: String,
        request: String,
        requesterAddress: String,
        escrowId: String,
        sequence: Long,
        epochId: Long,
        readTimeoutMs: Int?,
    ): V2InferenceResponse {
        val endpoint = java.net.URI("$publicUrl/v2/chat/completions").toURL()
        val connection = endpoint.openConnection() as HttpURLConnection
        connection.requestMethod = "POST"
        connection.connectTimeout = 5_000
        connection.readTimeout = readTimeoutMs ?: DEFAULT_READ_TIMEOUT_MS
        connection.setRequestProperty("X-Requester-Address", requesterAddress)
        connection.setRequestProperty("X-Escrow-Id", escrowId)
        connection.setRequestProperty("X-Escrow-Sequence", sequence.toString())
        connection.setRequestProperty("X-Epoch-Id", epochId.toString())
        connection.setRequestProperty("Content-Type", "application/json")
        connection.doOutput = true
        connection.outputStream.use { outputStream ->
            outputStream.write(request.toByteArray(Charsets.UTF_8))
            outputStream.flush()
        }

        try {
            val statusCode = connection.responseCode
            val body = (if (statusCode in 200..299) connection.inputStream else connection.errorStream)
                ?.bufferedReader()
                ?.use { it.readText() }
                .orEmpty()
            if (statusCode !in 200..299) {
                val relayArtifact = if (statusCode == HttpURLConnection.HTTP_UNAVAILABLE) {
                    runCatching { parseV2RelayErrorArtifact(body) }.getOrNull()
                } else {
                    null
                }
                throw V2RequestHttpException(
                    statusCode = statusCode,
                    body = body,
                    relayArtifact = relayArtifact,
                )
            }
            val openAIResponse = cosmosJson.fromJson(body, OpenAIResponse::class.java)
            val latestBlockSequence = connection.getHeaderField("X-Latest-Block-Sequence")
                ?.toLongOrNull()
                ?: error("Missing or invalid X-Latest-Block-Sequence header in v2 response")
            return V2InferenceResponse(
                openAIResponse = openAIResponse,
                latestBlockSequence = latestBlockSequence,
                responsePayloadHash = sha256Hex(body.toByteArray(Charsets.UTF_8)),
                executorAddress = connection.getHeaderField("X-V2-Executor-Address"),
                executorSignerAddress = connection.getHeaderField("X-V2-Executor-Signer-Address"),
                executorSignerPubKey = connection.getHeaderField("X-V2-Executor-Signer-PubKey"),
                executorSignature = connection.getHeaderField("X-V2-Executor-Signature"),
            )
        } finally {
            connection.disconnect()
        }
    }

    private fun recordReceiverAcknowledgment(recipientAddress: String, latestBlockSequence: Long) = lock.withLock {
        val current = acknowledgedByRecipient[recipientAddress] ?: 0L
        if (latestBlockSequence > current) {
            acknowledgedByRecipient[recipientAddress] = latestBlockSequence
        }
    }

    private fun recordFinishInference(
        requestSequence: Long,
        openAiResponse: OpenAIResponse?,
        responsePayloadHash: String,
        executorAddress: String?,
        executorSignerAddress: String?,
        executorSignerPubKey: String?,
        executorSignature: String?,
        status: String = "finished",
        inputTokenCountOverride: Long? = null,
        outputTokenCountOverride: Long? = null,
    ) = lock.withLock {
        require(responsePayloadHash.isNotBlank()) { "responsePayloadHash must not be blank" }
        require(!executorAddress.isNullOrBlank()) { "executorAddress must not be blank" }
        require(!executorSignerAddress.isNullOrBlank()) { "executorSignerAddress must not be blank" }
        require(!executorSignerPubKey.isNullOrBlank()) { "executorSignerPubKey must not be blank" }
        require(!executorSignature.isNullOrBlank()) { "executorSignature must not be blank" }

        val requestBlockSignature = chainBlocks
            .firstOrNull { it.blockSequence == requestSequence }
            ?.signature
            ?: error("Missing request block signature for sequence=$requestSequence")
        verifyExecutorProof(
            requestBlockSignature = requestBlockSignature,
            responsePayloadHash = responsePayloadHash,
            executorSignerPubKey = executorSignerPubKey,
            executorSignature = executorSignature,
        )

        val requestId = buildRequestId(context.escrowId, requestSequence)
        val inputTokenCount = inputTokenCountOverride ?: openAiResponse?.usage?.promptTokens?.toLong() ?: 0L
        val outputTokenCount = outputTokenCountOverride ?: openAiResponse?.usage?.completionTokens?.toLong() ?: 0L
        Logger.info(
            "Queued FinishInference request_id={} sequence={} status={} input_tokens={} output_tokens={} response_payload_hash={}",
            requestId,
            requestSequence,
            status,
            inputTokenCount,
            outputTokenCount,
            responsePayloadHash,
        )
        pendingFinishMessagesByRequestSequence[requestSequence] = DeveloperChainMessage(
            type = FINISH_INFERENCE_MESSAGE_TYPE,
            requestId = requestId,
            status = status,
            responsePayloadHash = responsePayloadHash,
            executorAddress = executorAddress,
            executorSignerAddress = executorSignerAddress,
            executorSignerPubKey = executorSignerPubKey,
            executorSignature = executorSignature,
            inputTokenCount = inputTokenCount,
            outputTokenCount = outputTokenCount,
            timestamp = nowSeconds(),
        )
    }

    private fun recordMissedInferenceInternal(
        requestSequence: Long,
        relayErrors: List<V2RelayErrorArtifact>,
    ) = lock.withLock {
        require(relayErrors.isNotEmpty()) { "MissedInference relay_errors must not be empty" }
        val requestId = buildRequestId(context.escrowId, requestSequence)
        val evidence = buildMissedInferenceEvidenceJson(relayErrors)
        Logger.info(
            "Queued MissedInference evidence request_id={} relay_count={} evidence={}",
            requestId,
            relayErrors.size,
            evidence,
        )
        pendingMissedMessagesByRequestSequence[requestSequence] = DeveloperChainMessage(
            type = MISSED_INFERENCE_MESSAGE_TYPE,
            requestId = requestId,
            missedInferenceEvidence = evidence,
            timestamp = nowSeconds(),
        )
    }

    private fun maybeQueueMissedInference(
        reserved: ReservedRequestState,
        relayErrors: List<V2RelayErrorArtifact>,
    ): Boolean {
        if (relayErrors.isEmpty()) {
            return false
        }
        val intendedExecutorAddress = reserved.responsibleParticipants.firstOrNull() ?: return false
        val responsibleParticipantSet = reserved.responsibleParticipants.toSet()
        val validRelayArtifacts = relayErrors
            .filter { artifact ->
                artifact.escrowId == context.escrowId &&
                    artifact.requestId == reserved.requestId &&
                    artifact.intendedExecutorAddress == intendedExecutorAddress &&
                    artifact.relayAddress in responsibleParticipantSet &&
                    artifact.relayAddress != intendedExecutorAddress &&
                    runCatching { verifyV2RelayErrorArtifactSignature(artifact) }.getOrDefault(false)
            }
            .distinctBy { it.relayAddress }
        val hasQuorum = validRelayArtifacts.size > reserved.responsibleParticipants.size / 2
        if (!hasQuorum) {
            return false
        }
        recordMissedInferenceInternal(
            requestSequence = reserved.sequence,
            relayErrors = validRelayArtifacts,
        )
        return true
    }

    private fun verifyExecutorProof(
        requestBlockSignature: String,
        responsePayloadHash: String,
        executorSignerPubKey: String,
        executorSignature: String,
    ) {
        val signingPayload = buildExecutorProofSigningPayload(
            developerRequestBlockSignature = requestBlockSignature,
            responsePayloadHash = responsePayloadHash,
        )
        val pubKeyBytes = Base64.getDecoder().decode(executorSignerPubKey)
        val signatureBytes = Base64.getDecoder().decode(executorSignature)
        check(verifySecp256k1Signature(signingPayload, pubKeyBytes, signatureBytes)) {
            "Executor proof signature verification failed"
        }
    }

    private fun resolveInitialRecipient(
        reserved: ReservedRequestState,
        preferredInitialRecipient: String?,
    ): String? {
        if (preferredInitialRecipient == null) {
            return reserved.intendedRecipientAddress
        }
        return preferredInitialRecipient.takeIf { it in reserved.responsibleParticipants }
    }

    private fun invalidRecipientFailure(
        reserved: ReservedRequestState,
        preferredInitialRecipient: String?,
    ): ClientFailure {
        return ClientFailure(
            error = ClientError(
                code = "INVALID_INITIAL_RECIPIENT",
                message = "Recipient $preferredInitialRecipient is not in responsible set ${reserved.responsibleParticipants}",
                retryable = false,
            ),
            retryHandle = buildRetryHandle(reserved),
        )
    }

    private fun resolveLeaderTimeoutMs(options: RequestOptions): Int? {
        return options.leaderTimeout?.toMillis()?.toInt()
            ?: options.timeout?.toMillis()?.toInt()
    }

    private fun buildRetryHandle(reserved: ReservedRequestState): RetryHandle {
        return RetryHandle(
            escrowId = context.escrowId,
            sequence = reserved.sequence,
            requestId = reserved.requestId,
            responsibleParticipants = reserved.responsibleParticipants,
            requestPayloadHash = reserved.requestPayloadHash,
            stream = reserved.openAiRequest.stream,
            requestStateRef = reserved.requestStateRef,
        )
    }

    private fun clientError(
        code: String,
        cause: Throwable,
        retryable: Boolean,
    ): ClientError {
        return ClientError(
            code = code,
            message = cause.message ?: cause::class.simpleName.orEmpty(),
            retryable = retryable,
            cause = cause,
        )
    }

    private fun nowSeconds(): Long = config.clock.instant().epochSecond
}

private sealed interface DispatchAttempt {
    data class Success(
        val reserved: ReservedRequestState,
        val recipientAddress: String,
        val response: V2InferenceResponse,
        val trace: AttemptTrace,
    ) : DispatchAttempt

    data class Failure(
        val trace: AttemptTrace,
        val cause: Throwable,
        val relayArtifact: V2RelayErrorArtifact? = null,
    ) : DispatchAttempt
}

private class V2RequestHttpException(
    val statusCode: Int,
    val body: String,
    val relayArtifact: V2RelayErrorArtifact? = null,
) : RuntimeException("V2 request failed: status=$statusCode body=$body")

private data class ReservedRequestState(
    val requestStateRef: String,
    val requestId: String,
    val sequence: Long,
    val requestPayloadHash: String,
    val openAiRequest: InferenceRequestPayload,
    val responsibleParticipants: List<String>,
    val intendedRecipientAddress: String,
)

private fun buildRequestId(escrowId: String, sequence: Long): String = "$escrowId:$sequence"

private fun computeRequestPayloadHash(openAiRequest: InferenceRequestPayload): String {
    val openAiRequestJson = cosmosJson.toJson(openAiRequest)
    return sha256Hex(openAiRequestJson.toByteArray(Charsets.UTF_8))
}

private fun computeResponsePayloadHash(openAiResponse: OpenAIResponse): String {
    val openAiResponseJson = cosmosJson.toJson(openAiResponse)
    return sha256Hex(openAiResponseJson.toByteArray(Charsets.UTF_8))
}

private fun isRetryableTransportFailure(cause: Throwable): Boolean {
    val chain = generateSequence(cause) { it.cause }.toList()
    if (chain.any { it is SocketTimeoutException }) {
        return true
    }
    val message = chain.joinToString(" ") { it.message.orEmpty() }.lowercase()
    return message.contains("timed out") ||
        message.contains("timeout") ||
        message.contains("connection refused") ||
        message.contains("status=503") ||
        message.contains("service unavailable")
}

private fun selectResponsibleParticipantsDeterministic(
    participants: List<V2WeightedParticipant>,
    selectionCount: Int,
    escrowId: String,
    sequence: Long,
): List<String> {
    require(selectionCount > 0) { "selectionCount must be > 0" }
    val eligibleParticipants = participants
        .filter { it.address.isNotBlank() && it.weight > 0uL }
        .sortedBy { it.address }
        .toMutableList()
    require(eligibleParticipants.isNotEmpty()) { "No eligible participants for deterministic selection" }

    val effectiveSelectionCount = minOf(selectionCount, eligibleParticipants.size)
    val seedHash = MessageDigest.getInstance("SHA-256")
        .digest("$escrowId:$sequence".toByteArray(Charsets.UTF_8))

    val responsibleParticipants = mutableListOf<String>()
    repeat(effectiveSelectionCount) { drawIndex ->
        val totalWeight = eligibleParticipants.fold(0uL) { acc, participant -> acc + participant.weight }
        require(totalWeight > 0uL) { "Eligible participants have zero total weight" }
        val ticket = deterministicWeightTicket(seedHash, drawIndex.toULong()) % totalWeight
        var cumulativeWeight = 0uL
        var selectedIndex = eligibleParticipants.lastIndex
        for ((idx, participant) in eligibleParticipants.withIndex()) {
            cumulativeWeight += participant.weight
            if (ticket < cumulativeWeight) {
                selectedIndex = idx
                break
            }
        }
        responsibleParticipants += eligibleParticipants[selectedIndex].address
        eligibleParticipants.removeAt(selectedIndex)
    }
    return responsibleParticipants
}

private fun deterministicWeightTicket(seedHash: ByteArray, drawIndex: ULong): ULong {
    val drawInput = ByteBuffer.allocate(40)
    drawInput.put(seedHash.copyOf(32))
    drawInput.putLong(drawIndex.toLong())
    val drawHash = MessageDigest.getInstance("SHA-256").digest(drawInput.array())
    return ByteBuffer.wrap(drawHash, 0, 8).long.toULong()
}

private data class DeveloperChainEnvelope(
    @SerializedName("openai_request")
    val openAiRequest: InferenceRequestPayload,
    val developerChainDelta: DeveloperChainDelta,
)

private data class DeveloperChainDelta(
    val baseBlockSequence: Long,
    val blocks: List<DeveloperChainBlock>,
    val latestBlockSequence: Long,
)

private data class SignedDeveloperChainBlock(
    val block: DeveloperChainBlock,
    val nextState: DeterministicChainState,
)

private data class DeterministicChainState(
    val executorStats: MutableMap<String, DeterministicExecutorStats> = mutableMapOf(),
)

private data class DeterministicExecutorStats(
    val processedInferences: Long = 0L,
    val inputTokenTotal: Long = 0L,
    val outputTokenTotal: Long = 0L,
    val missedInferences: Long = 0L,
)

private data class DeveloperChainBlock(
    val blockSequence: Long,
    val escrowId: String,
    @SerializedName("state_hash")
    val stateHash: String,
    val messages: List<DeveloperChainMessage>,
    val signature: String,
)

private data class DeveloperChainMessage(
    val type: String,
    val requestId: String,
    val modelId: String? = null,
    val requestPayloadHash: String? = null,
    val responsePayloadHash: String? = null,
    val executorAddress: String? = null,
    val executorSignerAddress: String? = null,
    @SerializedName("executor_signer_pubkey")
    val executorSignerPubKey: String? = null,
    val executorSignature: String? = null,
    val inputTokenCount: Long? = null,
    val outputTokenCount: Long? = null,
    val missedInferenceEvidence: String? = null,
    val status: String? = null,
    val timestamp: Long,
)

private data class V2MissedInferenceEvidencePayload(
    @SerializedName("relay_errors")
    val relayErrors: List<V2RelayErrorArtifact> = emptyList(),
)

private fun signDeveloperChainBlockWithState(
    blockSequence: Long,
    escrowId: String,
    messages: List<DeveloperChainMessage>,
    developerBlockSigner: V2DeveloperBlockSigner,
    baseState: DeterministicChainState = DeterministicChainState(),
): SignedDeveloperChainBlock {
    val nextState = applyDeterministicState(baseState, messages)
    val stateHash = computeDeterministicStateHash(nextState)
    val blockMessagesHash = computeDeveloperBlockMessagesHash(messages)
    val preimage = buildDeveloperBlockSigningPreimage(
        chainId = developerBlockSigner.chainId,
        escrowId = escrowId,
        blockSequence = blockSequence,
        blockMessagesHash = blockMessagesHash,
        stateHash = stateHash,
    )
    val preimageHashHex = sha256Hex(preimage)
    val signature = developerBlockSigner.signPayloadHex(preimageHashHex)
    return SignedDeveloperChainBlock(
        block = DeveloperChainBlock(
            blockSequence = blockSequence,
            escrowId = escrowId,
            stateHash = stateHash,
            messages = messages,
            signature = signature,
        ),
        nextState = nextState,
    )
}

private fun signDeveloperChainBlockWithExplicitStateHash(
    blockSequence: Long,
    escrowId: String,
    messages: List<DeveloperChainMessage>,
    developerBlockSigner: V2DeveloperBlockSigner,
    stateHash: String,
    nextState: DeterministicChainState,
): SignedDeveloperChainBlock {
    val blockMessagesHash = computeDeveloperBlockMessagesHash(messages)
    val preimage = buildDeveloperBlockSigningPreimage(
        chainId = developerBlockSigner.chainId,
        escrowId = escrowId,
        blockSequence = blockSequence,
        blockMessagesHash = blockMessagesHash,
        stateHash = stateHash,
    )
    val preimageHashHex = sha256Hex(preimage)
    val signature = developerBlockSigner.signPayloadHex(preimageHashHex)
    return SignedDeveloperChainBlock(
        block = DeveloperChainBlock(
            blockSequence = blockSequence,
            escrowId = escrowId,
            stateHash = stateHash,
            messages = messages,
            signature = signature,
        ),
        nextState = nextState,
    )
}

private fun applyDeterministicState(
    baseState: DeterministicChainState,
    messages: List<DeveloperChainMessage>,
): DeterministicChainState {
    val nextState = baseState.copy(executorStats = baseState.executorStats.toMutableMap())
    messages.forEach { message ->
        when (message.type) {
            START_INFERENCE_MESSAGE_TYPE -> Unit
            FINISH_INFERENCE_MESSAGE_TYPE -> {
                val executor = message.executorAddress ?: return@forEach
                val current = nextState.executorStats[executor] ?: DeterministicExecutorStats()
                nextState.executorStats[executor] = current.copy(
                    processedInferences = current.processedInferences + 1L,
                    inputTokenTotal = current.inputTokenTotal + (message.inputTokenCount ?: 0L),
                    outputTokenTotal = current.outputTokenTotal + (message.outputTokenCount ?: 0L),
                )
            }
            MISSED_INFERENCE_MESSAGE_TYPE -> {
                val intendedExecutor = extractMissedInferenceIntendedExecutor(message.missedInferenceEvidence ?: "")
                if (intendedExecutor.isNotBlank()) {
                    val current = nextState.executorStats[intendedExecutor] ?: DeterministicExecutorStats()
                    nextState.executorStats[intendedExecutor] = current.copy(
                        missedInferences = current.missedInferences + 1L,
                    )
                }
            }
        }
    }
    return nextState
}

private fun extractMissedInferenceIntendedExecutor(rawEvidence: String): String {
    return runCatching {
        val evidence = cosmosJson.fromJson(rawEvidence, V2MissedInferenceEvidencePayload::class.java)
        evidence.relayErrors.firstOrNull()?.intendedExecutorAddress.orEmpty()
    }.getOrDefault("")
}

private fun buildMissedInferenceEvidenceJson(relayErrors: List<V2RelayErrorArtifact>): String {
    val evidence = com.google.gson.JsonObject()
    val relayErrorsArray = com.google.gson.JsonArray()
    relayErrors.forEach { artifact ->
        relayErrorsArray.add(com.google.gson.JsonObject().apply {
            addProperty("escrow_id", artifact.escrowId)
            addProperty("request_id", artifact.requestId)
            addProperty("intended_executor_address", artifact.intendedExecutorAddress)
            addProperty("relay_address", artifact.relayAddress)
            addProperty("failure_code", artifact.failureCode)
            addProperty("relay_signer_address", artifact.relaySignerAddress)
            addProperty("relay_signer_pubkey", artifact.relaySignerPubKey)
            addProperty("relay_signature", artifact.relaySignature)
            addProperty("timestamp", artifact.timestamp)
        })
    }
    evidence.add("relay_errors", relayErrorsArray)
    return evidence.toString()
}

private fun computeDeterministicStateHash(state: DeterministicChainState): String {
    val output = ByteArrayOutputStream()
    writeLengthPrefixedString(output, DEV_STATE_HASH_DOMAIN)
    val ordered = state.executorStats.toSortedMap()
    writeInt64(output, ordered.size.toLong())
    ordered.forEach { (executorAddress, stats) ->
        writeLengthPrefixedString(output, executorAddress)
        writeInt64(output, stats.processedInferences)
        writeInt64(output, stats.inputTokenTotal)
        writeInt64(output, stats.outputTokenTotal)
        writeInt64(output, stats.missedInferences)
    }
    return sha256Hex(output.toByteArray())
}

private fun computeDeveloperBlockMessagesHash(messages: List<DeveloperChainMessage>): ByteArray {
    val aggregate = ByteArrayOutputStream()
    messages.forEach { message ->
        val messageHash = sha256Bytes(canonicalDeveloperChainMessageBytes(message))
        aggregate.write(messageHash)
    }
    return sha256Bytes(aggregate.toByteArray())
}

private fun canonicalDeveloperChainMessageBytes(message: DeveloperChainMessage): ByteArray {
    val output = ByteArrayOutputStream()
    writeLengthPrefixedString(output, DEV_BLOCK_MESSAGE_DOMAIN)
    writeLengthPrefixedString(output, message.type)
    writeLengthPrefixedString(output, message.requestId)
    writeLengthPrefixedString(output, message.modelId ?: "")
    writeLengthPrefixedString(output, message.requestPayloadHash ?: "")
    writeLengthPrefixedString(output, message.responsePayloadHash ?: "")
    writeLengthPrefixedString(output, message.executorAddress ?: "")
    writeLengthPrefixedString(output, message.executorSignerAddress ?: "")
    writeLengthPrefixedString(output, message.executorSignerPubKey ?: "")
    writeLengthPrefixedString(output, message.executorSignature ?: "")
    writeInt64(output, message.inputTokenCount ?: 0L)
    writeInt64(output, message.outputTokenCount ?: 0L)
    writeLengthPrefixedString(output, message.missedInferenceEvidence ?: "")
    writeLengthPrefixedString(output, message.status ?: "")
    writeInt64(output, message.timestamp)
    return output.toByteArray()
}

private fun buildDeveloperBlockSigningPreimage(
    chainId: String,
    escrowId: String,
    blockSequence: Long,
    blockMessagesHash: ByteArray,
    stateHash: String,
): ByteArray {
    val output = ByteArrayOutputStream()
    writeLengthPrefixedString(output, DEV_BLOCK_SIGN_DOMAIN)
    writeLengthPrefixedString(output, chainId)
    writeLengthPrefixedString(output, escrowId)
    writeInt64(output, blockSequence)
    output.write(blockMessagesHash)
    writeLengthPrefixedString(output, stateHash)
    return output.toByteArray()
}

private fun buildExecutorProofSigningPayload(
    developerRequestBlockSignature: String,
    responsePayloadHash: String,
): ByteArray {
    val preimage = ByteArrayOutputStream()
    writeLengthPrefixedString(preimage, EXEC_FINISH_SIGN_DOMAIN)
    writeLengthPrefixedString(preimage, developerRequestBlockSignature)
    writeLengthPrefixedString(preimage, responsePayloadHash)
    return sha256Hex(preimage.toByteArray()).toByteArray(Charsets.UTF_8)
}

private data class V2RelayErrorEnvelope(
    val error: V2RelayErrorArtifact?,
)

fun parseV2RelayErrorArtifact(rawBody: String): V2RelayErrorArtifact {
    val envelope = cosmosJson.fromJson(rawBody, V2RelayErrorEnvelope::class.java)
    return requireNotNull(envelope.error) { "Missing relay error artifact in body: $rawBody" }
}

fun verifyV2RelayErrorArtifactSignature(artifact: V2RelayErrorArtifact): Boolean {
    val signingPayload = buildRelayErrorSigningPayload(artifact)
    return verifySecp256k1Signature(
        signingPayload = signingPayload,
        pubKeyBytes = Base64.getDecoder().decode(artifact.relaySignerPubKey),
        signatureBytes = Base64.getDecoder().decode(artifact.relaySignature),
    )
}

private fun buildRelayErrorSigningPayload(artifact: V2RelayErrorArtifact): ByteArray {
    val preimage = ByteArrayOutputStream()
    writeLengthPrefixedString(preimage, RELAY_ERROR_SIGN_DOMAIN)
    writeLengthPrefixedString(preimage, artifact.escrowId)
    writeLengthPrefixedString(preimage, artifact.requestId)
    writeLengthPrefixedString(preimage, artifact.intendedExecutorAddress)
    writeLengthPrefixedString(preimage, artifact.relayAddress)
    writeLengthPrefixedString(preimage, artifact.failureCode)
    writeLengthPrefixedString(preimage, artifact.relaySignerAddress)
    writeLengthPrefixedString(preimage, artifact.relaySignerPubKey)
    writeInt64(preimage, artifact.timestamp)
    return sha256Hex(preimage.toByteArray()).toByteArray(Charsets.UTF_8)
}

private fun verifySecp256k1Signature(
    signingPayload: ByteArray,
    pubKeyBytes: ByteArray,
    signatureBytes: ByteArray,
): Boolean {
    return try {
        val curve = SECNamedCurves.getByName("secp256k1")
        val domain = ECDomainParameters(curve.curve, curve.g, curve.n, curve.h)
        val publicPoint = curve.curve.decodePoint(pubKeyBytes)
        val publicKey = ECPublicKeyParameters(publicPoint, domain)
        val signer = ECDSASigner()
        signer.init(false, publicKey)
        val signatureParts = decodeEcdsaSignature(signatureBytes) ?: return false
        val sha256 = MessageDigest.getInstance("SHA-256")
        val maybeHexDecoded = runCatching { hexToBytes(String(signingPayload, Charsets.UTF_8)) }.getOrNull()
        val candidatePayloads = listOfNotNull(
            signingPayload,
            sha256.digest(signingPayload),
            maybeHexDecoded,
            maybeHexDecoded?.let { sha256.digest(it) },
        ).distinctBy { it.joinToString(",") }
        for (candidate in candidatePayloads) {
            if (signer.verifySignature(candidate, signatureParts.first, signatureParts.second)) {
                return true
            }
        }
        val bitcoinSig = ECKey.ECDSASignature(signatureParts.first, signatureParts.second)
        val bitcoinKey = ECKey.fromPublicOnly(pubKeyBytes)
        candidatePayloads
            .filter { it.size == 32 }
            .any { candidate -> bitcoinKey.verify(Sha256Hash.wrap(candidate), bitcoinSig) }
    } catch (_: Exception) {
        false
    }
}

private fun decodeEcdsaSignature(signatureBytes: ByteArray): Pair<BigInteger, BigInteger>? {
    if (signatureBytes.size == 64) {
        val r = BigInteger(1, signatureBytes.copyOfRange(0, 32))
        val s = BigInteger(1, signatureBytes.copyOfRange(32, 64))
        return r to s
    }
    return try {
        val sequence = ASN1Primitive.fromByteArray(signatureBytes) as? ASN1Sequence ?: return null
        if (sequence.size() != 2) {
            return null
        }
        val r = (sequence.getObjectAt(0) as ASN1Integer).positiveValue
        val s = (sequence.getObjectAt(1) as ASN1Integer).positiveValue
        r to s
    } catch (_: Exception) {
        null
    }
}

private fun hexToBytes(hex: String): ByteArray {
    val clean = hex.trim()
    require(clean.length % 2 == 0) { "hex length must be even" }
    return ByteArray(clean.length / 2) { idx ->
        clean.substring(idx * 2, idx * 2 + 2).toInt(16).toByte()
    }
}

private fun sha256Hex(input: ByteArray): String {
    return sha256Bytes(input).joinToString("") { byte -> "%02x".format(byte.toInt() and 0xFF) }
}

private fun sha256Bytes(input: ByteArray): ByteArray {
    return MessageDigest.getInstance("SHA-256").digest(input)
}

private fun writeLengthPrefixedString(output: ByteArrayOutputStream, value: String) {
    val bytes = value.toByteArray(Charsets.UTF_8)
    output.write(ByteBuffer.allocate(4).putInt(bytes.size).array())
    output.write(bytes)
}

private fun writeInt64(output: ByteArrayOutputStream, value: Long) {
    output.write(ByteBuffer.allocate(8).putLong(value).array())
}

private data class StreamQueueLine(val line: String)

private object StreamQueueEnd

private data class ManagedStreamCompletion(
    val responsePayloadHash: String,
    val executorProof: V2ExecutorProof,
    val inputTokenCount: Long,
    val outputTokenCount: Long,
)

private class ManagedV2ClientStream(
    private val rawStream: LineReadableStream,
    private val onCompleted: (ManagedStreamCompletion) -> Unit,
) : LineReadableStream {
    private val queue = LinkedBlockingQueue<Any>()
    private val finished = CountDownLatch(1)
    @Volatile
    private var closed = false
    @Volatile
    private var completionSeen = false
    @Volatile
    private var finalizationError: Throwable? = null

    init {
        val readerThread = Thread({
            pump()
        }, "managed-v2-client-stream")
        readerThread.isDaemon = true
        readerThread.start()
    }

    override fun readLine(): String? {
        if (closed) return null
        return when (val item = queue.take()) {
            is StreamQueueLine -> item.line
            StreamQueueEnd -> null
            else -> null
        }
    }

    override fun close() {
        if (closed) return
        closed = true
        if (!completionSeen) {
            runCatching { rawStream.close() }
        }
        finished.await(5, TimeUnit.SECONDS)
        finalizationError?.let { throw IllegalStateException("Stream finalization failed", it) }
    }

    private fun pump() {
        val hashedStream = ByteArrayOutputStream()
        var currentEvent = "message"
        var executorProof: V2ExecutorProof? = null
        var responsePayloadHash: String? = null
        var promptTokenCount = 0L
        var completionTokenCount = 0L
        var aborted = false
        try {
            while (true) {
                val line = rawStream.readLine() ?: break
                queue.put(StreamQueueLine(line))
                val trimmed = line.trim()
                if (trimmed.startsWith("event:")) {
                    currentEvent = trimmed.removePrefix("event:").trim()
                    if (currentEvent != V2_EXECUTOR_PROOF_EVENT) {
                        hashedStream.write((line + "\n").toByteArray(Charsets.UTF_8))
                    }
                    continue
                }
                if (trimmed.startsWith("data:")) {
                    val payload = trimmed.removePrefix("data:").trimStart()
                    if (currentEvent == V2_EXECUTOR_PROOF_EVENT) {
                        executorProof = runCatching {
                            cosmosJson.fromJson(payload, V2ExecutorProof::class.java)
                        }.getOrNull()
                        continue
                    }
                    extractStreamUsageCounts(payload)?.let { usage ->
                        promptTokenCount = usage.first
                        completionTokenCount = usage.second
                    }
                    hashedStream.write((line + "\n").toByteArray(Charsets.UTF_8))
                    if (payload == "[DONE]") {
                        completionSeen = true
                        responsePayloadHash = sha256Hex(hashedStream.toByteArray())
                    }
                    continue
                }
                if (currentEvent != V2_EXECUTOR_PROOF_EVENT) {
                    hashedStream.write((line + "\n").toByteArray(Charsets.UTF_8))
                }
                if (trimmed.isEmpty()) {
                    currentEvent = "message"
                }
            }
            if (!closed && completionSeen && responsePayloadHash != null && executorProof != null) {
                onCompleted(
                    ManagedStreamCompletion(
                        responsePayloadHash = responsePayloadHash!!,
                        executorProof = executorProof!!,
                        inputTokenCount = promptTokenCount,
                        outputTokenCount = completionTokenCount,
                    )
                )
            } else if (completionSeen && responsePayloadHash != null && executorProof == null) {
                finalizationError = IllegalStateException("Missing terminal executor proof for completed stream")
            } else if (!completionSeen) {
                aborted = true
            }
        } catch (t: Throwable) {
            if (!closed) {
                finalizationError = t
            } else if (!completionSeen) {
                aborted = true
            }
        } finally {
            if (aborted) {
                Logger.info("Managed v2 stream closed before completion; FinishInference will not be recorded")
            }
            runCatching { rawStream.close() }
            queue.offer(StreamQueueEnd)
            finished.countDown()
        }
    }
}

private fun extractStreamUsageCounts(payload: String): Pair<Long, Long>? {
    if (payload == "[DONE]") {
        return null
    }
    return runCatching {
        val json = cosmosJson.fromJson(payload, com.google.gson.JsonObject::class.java)
        val usage = json.getAsJsonObject("usage") ?: return@runCatching null
        val promptTokens = usage.get("prompt_tokens")?.asLong ?: 0L
        val completionTokens = usage.get("completion_tokens")?.asLong ?: 0L
        promptTokens to completionTokens
    }.getOrNull()
}

private const val DEFAULT_READ_TIMEOUT_MS = 60_000
private const val START_INFERENCE_MESSAGE_TYPE = "StartInference"
private const val FINISH_INFERENCE_MESSAGE_TYPE = "FinishInference"
private const val MISSED_INFERENCE_MESSAGE_TYPE = "MissedInference"
private const val DEV_BLOCK_MESSAGE_DOMAIN = "v2_dev_block_msg_v1"
private const val DEV_BLOCK_SIGN_DOMAIN = "v2_dev_block_sig_v1"
private const val DEV_STATE_HASH_DOMAIN = "v2_dev_state_hash_v1"
private const val EXEC_FINISH_SIGN_DOMAIN = "v2_exec_finish_sig_v1"
private const val RELAY_ERROR_SIGN_DOMAIN = "v2_relay_error_sig_v1"
private const val V2_EXECUTOR_PROOF_EVENT = "v2_executor_proof"
