import com.productscience.LocalInferencePair
import com.productscience.V2InferenceResponse
import com.productscience.V2InferenceStreamConnection
import com.productscience.InferenceRequestPayload
import com.productscience.cosmosJson
import com.productscience.defaultInferenceResponseObject
import com.productscience.defaultModel
import com.productscience.inferenceRequestObject
import com.productscience.inferenceConfig
import com.productscience.initCluster
import com.productscience.logSection
import com.productscience.data.MsgCreateEscrow
import com.productscience.data.TxResponse
import com.github.dockerjava.core.DockerClientBuilder
import com.google.gson.annotations.SerializedName
import org.bitcoinj.core.ECKey
import org.bitcoinj.core.Sha256Hash
import org.assertj.core.api.Assertions.assertThat
import org.assertj.core.api.Assertions.assertThatThrownBy
import org.bouncycastle.asn1.ASN1Integer
import org.bouncycastle.asn1.ASN1Primitive
import org.bouncycastle.asn1.ASN1Sequence
import org.bouncycastle.asn1.sec.SECNamedCurves
import org.bouncycastle.crypto.params.ECDomainParameters
import org.bouncycastle.crypto.params.ECPublicKeyParameters
import org.bouncycastle.crypto.signers.ECDSASigner
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.kotlin.Logger
import java.io.ByteArrayOutputStream
import java.math.BigInteger
import java.nio.ByteBuffer
import java.security.MessageDigest
import java.time.Duration
import java.net.HttpURLConnection
import java.net.SocketTimeoutException
import java.util.Base64
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.locks.ReentrantLock
import kotlin.concurrent.withLock

@Timeout(value = 15, unit = TimeUnit.MINUTES)
class V2EscrowFlowTest : TestermintTest() {
    @Test
    fun `developer uses v2 api with escrow across 3 participants`() {
        logSection("V2 escrow flow test setup")
        val fixture = setupV2EscrowFixture()
        val allPairs = fixture.allPairs
        val genesis = fixture.genesis
        val pairByAddress = fixture.pairByAddress
        val weightedParticipants = fixture.weightedParticipants
        val participantPubKeyByAddress = fixture.participantPubKeyByAddress
        val developerAddress = fixture.developerAddress
        val developerBlockSigner = fixture.developerBlockSigner

        logSection("Developer creates escrow")
        val escrowContext = createEscrowAndWaitForIndexing(genesis, allPairs, developerAddress)
        val escrowId = escrowContext.escrowId
        val epochId = escrowContext.epochId
        Logger.info(
            "V2_ESCROW_TEST created escrow_id={} developer={} participants={}",
            escrowId,
            developerAddress,
            pairByAddress.keys.sorted(),
        )

        logSection("Sending 10 v2 requests with deterministic executor choice")
        val planController = DeveloperChainController(
            escrowId = escrowId,
            modelId = defaultModel,
            weightedParticipants = weightedParticipants,
            participantPubKeyByAddress = participantPubKeyByAddress,
            developerBlockSigner = developerBlockSigner,
        )
        repeat(10) { requestIndex ->
            val openAiRequest = buildOpenAiRequest(sequence = requestIndex + 1L, stream = false)
            val plan = planController.reserveStartInference(
                requestPayloadHash = computeV2RequestPayloadHash(openAiRequest),
                timestampSeconds = System.currentTimeMillis() / 1000,
            )
            if (plan.sequence > 1L) {
                val expectedPreviousRequestId = buildV2RequestId(escrowId, plan.sequence - 1L)
                val finishMessages = plan.developerChainDelta.blocks
                    .last()
                    .messages
                    .filter { it.type == FINISH_INFERENCE_MESSAGE_TYPE }
                assertThat(finishMessages.any { it.requestId == expectedPreviousRequestId }).isTrue()
            }

            val chosenExecutorAddress = plan.recipientAddress
            val targetPair = requireNotNull(pairByAddress[chosenExecutorAddress]) {
                "Missing pair for executor address=$chosenExecutorAddress"
            }

            val request = buildV2RequestEnvelope(
                openAiRequest = openAiRequest,
                developerChainDelta = plan.developerChainDelta,
            )

            val response = sendV2Request(
                pair = targetPair,
                request = request,
                requesterAddress = developerAddress,
                escrowId = escrowId,
                sequence = plan.sequence,
                epochId = epochId,
            )

            Logger.info(
                "V2_ESCROW_TEST sequence={} chosen_executor={} responsible_participants={} response_id={}",
                plan.sequence,
                chosenExecutorAddress,
                selectResponsibleParticipantsDeterministic(
                    participants = weightedParticipants,
                    selectionCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
                    escrowId = escrowId,
                    sequence = plan.sequence,
                ),
                response.openAIResponse.id,
            )

            assertThat(response.openAIResponse.id).isNotBlank()
            assertThat(response.openAIResponse.model).isEqualTo(defaultModel)
            assertThat(response.openAIResponse.choices).isNotEmpty()
            assertThat(response.latestBlockSequence).isEqualTo(plan.sequence)
            planController.recordReceiverAcknowledgment(chosenExecutorAddress, response.latestBlockSequence)
            val responsePayloadHash = response.responsePayloadHash
                ?: computeV2ResponsePayloadHash(response.openAIResponse)
            planController.recordFinishInference(
                requestSequence = plan.sequence,
                openAiResponse = response.openAIResponse,
                responsePayloadHash = responsePayloadHash,
                executorAddress = response.executorAddress,
                executorSignerAddress = response.executorSignerAddress,
                executorSignerPubKey = response.executorSignerPubKey,
                executorSignature = response.executorSignature,
                timestampSeconds = System.currentTimeMillis() / 1000,
            )
        }

        logSection("Sending stale base_block_sequence request and expecting rejection")
        val invalidSequence = 11L
        val invalidResponsibleParticipants = selectResponsibleParticipantsDeterministic(
            participants = weightedParticipants,
            selectionCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
            escrowId = escrowId,
            sequence = invalidSequence,
        )
        val invalidTargetAddress = invalidResponsibleParticipants
            .maxByOrNull { planController.getReceiverAcknowledgedBlockSequence(it) }
            ?: invalidResponsibleParticipants.first()
        val invalidTargetPair = requireNotNull(pairByAddress[invalidTargetAddress]) {
            "Missing pair for invalid continuity request"
        }
        val receiverLatestBlockSequence = planController.getReceiverAcknowledgedBlockSequence(invalidTargetAddress)
        assertThat(receiverLatestBlockSequence).isGreaterThan(0L)
        val staleBaseBlockSequence = receiverLatestBlockSequence - 1
        val invalidOpenAiRequest = buildOpenAiRequest(sequence = invalidSequence, stream = false)
        val invalidRequest = buildV2RequestEnvelope(
            openAiRequest = invalidOpenAiRequest,
            developerChainDelta = DeveloperChainDelta(
                baseBlockSequence = staleBaseBlockSequence,
                blocks = listOf(
                    signDeveloperChainBlock(
                        blockSequence = staleBaseBlockSequence + 1,
                        escrowId = escrowId,
                        messages = listOf(
                            DeveloperChainMessage(
                                type = START_INFERENCE_MESSAGE_TYPE,
                                requestId = buildV2RequestId(escrowId, invalidSequence),
                                modelId = defaultModel,
                                requestPayloadHash = computeV2RequestPayloadHash(invalidOpenAiRequest),
                                timestamp = System.currentTimeMillis() / 1000,
                            )
                        ),
                        developerBlockSigner = developerBlockSigner,
                    ),
                ),
                latestBlockSequence = staleBaseBlockSequence + 1,
            )
        )

        assertThatThrownBy {
            sendV2Request(
                pair = invalidTargetPair,
                request = invalidRequest,
                requesterAddress = developerAddress,
                escrowId = escrowId,
                sequence = invalidSequence,
                epochId = epochId,
            )
        }.hasMessageContaining("409")
    }

    @Test
    fun `developer sends overlapping v2 streaming requests in parallel`() {
        logSection("V2 parallel streaming overlap test setup")
        val fixture = setupV2EscrowFixture()
        val allPairs = fixture.allPairs
        val genesis = fixture.genesis
        val pairByAddress = fixture.pairByAddress
        val weightedParticipants = fixture.weightedParticipants
        val participantPubKeyByAddress = fixture.participantPubKeyByAddress
        val developerAddress = fixture.developerAddress
        val developerBlockSigner = fixture.developerBlockSigner

        allPairs.forEach { pair ->
            pair.mock?.setInferenceResponse(
                openAIResponse = defaultInferenceResponseObject,
                delay = Duration.ofSeconds(5),
                streamDelay = Duration.ofMillis(50),
                model = defaultModel,
            )
        }

        val escrowContext = createEscrowAndWaitForIndexing(genesis, allPairs, developerAddress)
        val escrowId = escrowContext.escrowId
        val epochId = escrowContext.epochId

        val planController = DeveloperChainController(
            escrowId = escrowId,
            modelId = defaultModel,
            weightedParticipants = weightedParticipants,
            participantPubKeyByAddress = participantPubKeyByAddress,
            developerBlockSigner = developerBlockSigner,
        )
        val requestCount = 12

        val executor = Executors.newFixedThreadPool(requestCount)
        val startGate = CountDownLatch(1)
        try {
            val futures = (1..requestCount).map {
                executor.submit<V2StreamAcceptanceResult> {
                    startGate.await()
                    val openAiRequest = inferenceRequestObject.copy(stream = true)
                    val requestPayloadHash = computeV2RequestPayloadHash(openAiRequest)
                    val plan = planController.reserveStartInference(
                        requestPayloadHash = requestPayloadHash,
                        timestampSeconds = System.currentTimeMillis() / 1000,
                    )
                    val targetPair = requireNotNull(pairByAddress[plan.recipientAddress]) {
                        "Missing pair for recipient=${plan.recipientAddress}"
                    }
                    val request = buildV2RequestEnvelope(
                        openAiRequest = openAiRequest,
                        developerChainDelta = plan.developerChainDelta,
                    )
                    val streamConnection = openV2Stream(
                        pair = targetPair,
                        request = request,
                        requesterAddress = developerAddress,
                        escrowId = escrowId,
                        sequence = plan.sequence,
                        epochId = epochId,
                    )
                    planController.recordReceiverAcknowledgment(plan.recipientAddress, streamConnection.latestBlockSequence)
                    try {
                        V2StreamAcceptanceResult(
                            sequence = plan.sequence,
                            latestBlockSequence = streamConnection.latestBlockSequence,
                        )
                    } finally {
                        streamConnection.streamConnection.close()
                    }
                }
            }

            startGate.countDown()
            val results = futures
                .map { it.get(45, TimeUnit.SECONDS) }
                .sortedBy { it.sequence }

            assertThat(results).hasSize(requestCount)
            assertThat(results.map { it.sequence }).containsExactlyElementsOf((1L..requestCount.toLong()).toList())
            results.forEach { result ->
                assertThat(result.latestBlockSequence).isGreaterThanOrEqualTo(result.sequence)
            }
        } finally {
            executor.shutdownNow()
        }
    }

    @Test
    fun `developer rejects invalid executor proof before appending finish inference`() {
        logSection("V2 invalid executor proof rejection setup")
        val fixture = setupV2EscrowFixture()
        val allPairs = fixture.allPairs
        val genesis = fixture.genesis
        val pairByAddress = fixture.pairByAddress
        val weightedParticipants = fixture.weightedParticipants
        val participantPubKeyByAddress = fixture.participantPubKeyByAddress
        val developerAddress = fixture.developerAddress
        val developerBlockSigner = fixture.developerBlockSigner

        val escrowContext = createEscrowAndWaitForIndexing(genesis, allPairs, developerAddress)
        val escrowId = escrowContext.escrowId
        val epochId = escrowContext.epochId

        val planController = DeveloperChainController(
            escrowId = escrowId,
            modelId = defaultModel,
            weightedParticipants = weightedParticipants,
            participantPubKeyByAddress = participantPubKeyByAddress,
            developerBlockSigner = developerBlockSigner,
        )

        val openAiRequest = buildOpenAiRequest(sequence = 1L, stream = false)
        val requestPayloadHash = computeV2RequestPayloadHash(openAiRequest)
        val plan = planController.reserveStartInference(
            requestPayloadHash = requestPayloadHash,
            timestampSeconds = System.currentTimeMillis() / 1000,
        )
        val sequence = plan.sequence
        val request = buildV2RequestEnvelope(
            openAiRequest = openAiRequest,
            developerChainDelta = plan.developerChainDelta,
        )

        val intendedExecutor = selectResponsibleParticipantsDeterministic(
            participants = weightedParticipants,
            selectionCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
            escrowId = escrowId,
            sequence = sequence,
        ).first()
        val intendedPair = requireNotNull(pairByAddress[intendedExecutor]) {
            "Missing pair for intended executor=$intendedExecutor"
        }

        val response = sendV2Request(
            pair = intendedPair,
            request = request,
            requesterAddress = developerAddress,
            escrowId = escrowId,
            sequence = sequence,
            epochId = epochId,
        )

        val tamperedSignature = tamperBase64Signature(response.executorSignature!!)
        assertThatThrownBy {
            planController.recordFinishInference(
                requestSequence = sequence,
                openAiResponse = response.openAIResponse,
                responsePayloadHash = response.responsePayloadHash
                    ?: computeV2ResponsePayloadHash(response.openAIResponse),
                executorAddress = response.executorAddress,
                executorSignerAddress = response.executorSignerAddress,
                executorSignerPubKey = response.executorSignerPubKey,
                executorSignature = tamperedSignature,
                timestampSeconds = System.currentTimeMillis() / 1000,
            )
        }.isInstanceOf(AssertionError::class.java)
    }

    @Test
    fun `developer retries same logical request through all responsible participants`() {
        logSection("V2 relay retry/fanout test setup")
        val fixture = setupV2EscrowFixture()
        val allPairs = fixture.allPairs
        val genesis = fixture.genesis
        val pairByAddress = fixture.pairByAddress
        val weightedParticipants = fixture.weightedParticipants
        val developerAddress = fixture.developerAddress
        val developerBlockSigner = fixture.developerBlockSigner

        val escrowContext = createEscrowAndWaitForIndexing(genesis, allPairs, developerAddress)
        val escrowId = escrowContext.escrowId
        val epochId = escrowContext.epochId

        val sequence = 1L
        val openAiRequest = buildOpenAiRequest(sequence = sequence, stream = false)
        val requestPayloadHash = computeV2RequestPayloadHash(openAiRequest)
        val requestId = buildV2RequestId(escrowId, sequence)

        val responsibleParticipants = selectResponsibleParticipantsDeterministic(
            participants = weightedParticipants,
            selectionCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
            escrowId = escrowId,
            sequence = sequence,
        )
        assertThat(responsibleParticipants).hasSize(3)
        val intendedExecutor = responsibleParticipants.first()
        val intendedPair = requireNotNull(pairByAddress[intendedExecutor]) {
            "Missing pair for intended executor=$intendedExecutor"
        }
        intendedPair.mock?.setInferenceResponse(
            openAIResponse = defaultInferenceResponseObject,
            delay = Duration.ofSeconds(3),
            streamDelay = Duration.ofMillis(50),
            model = defaultModel,
        )

        val request = buildSingleStartEnvelope(
            openAiRequest = openAiRequest,
            escrowId = escrowId,
            sequence = sequence,
            developerBlockSigner = developerBlockSigner,
            requestPayloadHashOverride = requestPayloadHash,
        )
        val requestEnvelope = cosmosJson.fromJson(request, DeveloperChainEnvelope::class.java)
        val requestBlockSignature = requestEnvelope.developerChainDelta.blocks
            .firstOrNull { block ->
                block.messages.any { it.type == START_INFERENCE_MESSAGE_TYPE && it.requestId == buildV2RequestId(escrowId, sequence) }
            }
            ?.signature
            ?: error("Missing request block signature in stream request envelope")

        val pool = Executors.newFixedThreadPool(responsibleParticipants.size)
        val startGate = CountDownLatch(1)
        try {
            val futures = responsibleParticipants.map { participantAddress ->
                val pair = requireNotNull(pairByAddress[participantAddress]) {
                    "Missing pair for responsible participant=$participantAddress"
                }
                pool.submit<V2InferenceResponse> {
                    startGate.await()
                    sendV2Request(
                        pair = pair,
                        request = request,
                        requesterAddress = developerAddress,
                        escrowId = escrowId,
                        sequence = sequence,
                        epochId = epochId,
                    )
                }
            }
            startGate.countDown()

            val responses = futures.map { it.get(60, TimeUnit.SECONDS) }
            assertThat(responses).hasSize(responsibleParticipants.size)
            assertThat(responses.map { it.openAIResponse.id }.distinct()).hasSize(1)
            responses.forEach { response ->
                assertThat(response.latestBlockSequence).isEqualTo(sequence)
                assertThat(response.openAIResponse.id).isNotBlank()
            }

            // Replay after acceptance should remain deterministic for the same logical request identity.
            val replayResponse = requireNotNull(pairByAddress[responsibleParticipants[1]])
                .let { replayPair ->
                    sendV2Request(
                        pair = replayPair,
                        request = request,
                        requesterAddress = developerAddress,
                        escrowId = escrowId,
                        sequence = sequence,
                        epochId = epochId,
                    )
                }
            assertThat(replayResponse.openAIResponse.id).isEqualTo(responses.first().openAIResponse.id)
            assertThat(replayResponse.latestBlockSequence).isEqualTo(sequence)
            Logger.info(
                "V2_STEP11 fanout completed request_id={} intended_executor={} recipients={}",
                requestId,
                intendedExecutor,
                responsibleParticipants,
            )
        } finally {
            pool.shutdownNow()
        }
    }

    @Test
    fun `developer receives streamed replay from all responsible participants for same request id`() {
        logSection("V2 streaming fanout replay test setup")
        val fixture = setupV2EscrowFixture()
        val allPairs = fixture.allPairs
        val genesis = fixture.genesis
        val pairByAddress = fixture.pairByAddress
        val weightedParticipants = fixture.weightedParticipants
        val participantPubKeyByAddress = fixture.participantPubKeyByAddress
        val developerAddress = fixture.developerAddress
        val developerBlockSigner = fixture.developerBlockSigner
        allPairs.forEach { pair ->
            pair.mock?.setInferenceResponse(
                openAIResponse = defaultInferenceResponseObject,
                delay = Duration.ofSeconds(3),
                streamDelay = Duration.ofMillis(50),
                model = defaultModel,
            )
        }

        val escrowContext = createEscrowAndWaitForIndexing(genesis, allPairs, developerAddress)
        val escrowId = escrowContext.escrowId
        val epochId = escrowContext.epochId

        val planController = DeveloperChainController(
            escrowId = escrowId,
            modelId = defaultModel,
            weightedParticipants = weightedParticipants,
            participantPubKeyByAddress = participantPubKeyByAddress,
            developerBlockSigner = developerBlockSigner,
        )
        val sequence = 1L
        val openAiRequest = buildOpenAiRequest(sequence = sequence, stream = true)
        val requestPayloadHash = computeV2RequestPayloadHash(openAiRequest)
        val plan = planController.reserveStartInference(
            requestPayloadHash = requestPayloadHash,
            timestampSeconds = System.currentTimeMillis() / 1000,
        )
        assertThat(plan.sequence).isEqualTo(sequence)
        val request = buildV2RequestEnvelope(
            openAiRequest = openAiRequest,
            developerChainDelta = plan.developerChainDelta,
        )
        val requestEnvelope = cosmosJson.fromJson(request, DeveloperChainEnvelope::class.java)
        val requestBlockSignature = requestEnvelope.developerChainDelta.blocks
            .firstOrNull { block ->
                block.messages.any { it.type == START_INFERENCE_MESSAGE_TYPE && it.requestId == buildV2RequestId(escrowId, sequence) }
            }
            ?.signature
            ?: error("Missing request block signature in stream request envelope")

        val responsibleParticipants = selectResponsibleParticipantsDeterministic(
            participants = weightedParticipants,
            selectionCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
            escrowId = escrowId,
            sequence = sequence,
        )
        assertThat(responsibleParticipants).hasSize(3)

        val pool = Executors.newFixedThreadPool(responsibleParticipants.size)
        val startGate = CountDownLatch(1)
        try {
            val futures = responsibleParticipants.map { participantAddress ->
                val pair = requireNotNull(pairByAddress[participantAddress]) {
                    "Missing pair for responsible participant=$participantAddress"
                }
                pool.submit<StreamFanoutResult> {
                    startGate.await()
                    val connection = openV2Stream(
                        pair = pair,
                        request = request,
                        requesterAddress = developerAddress,
                        escrowId = escrowId,
                        sequence = sequence,
                        epochId = epochId,
                    )
                    try {
                        val streamResult = readV2StreamDataAndProof(connection.streamConnection)
                        StreamFanoutResult(
                            participantAddress = participantAddress,
                            latestBlockSequence = connection.latestBlockSequence,
                            dataLines = streamResult.dataLines,
                            executorProof = streamResult.executorProof,
                            responsePayloadHash = streamResult.responsePayloadHash,
                        )
                    } finally {
                        connection.streamConnection.close()
                    }
                }
            }

            startGate.countDown()
            val results = futures.map { it.get(60, TimeUnit.SECONDS) }
            assertThat(results).hasSize(responsibleParticipants.size)
            results.forEach { result ->
                assertThat(result.latestBlockSequence).isEqualTo(sequence)
                assertThat(result.dataLines).isNotEmpty()
                assertThat(result.executorProof).isNotNull()
                assertThat(result.executorProof?.executorAddress).isNotBlank()
                assertThat(result.executorProof?.executorSignerAddress).isNotBlank()
                assertThat(result.executorProof?.executorSignerPubKey).isNotBlank()
                assertThat(result.executorProof?.executorSignature).isNotBlank()
                assertThat(result.responsePayloadHash).isNotBlank()
                val streamSigningPayload = buildExecutorProofSigningPayload(
                    developerRequestBlockSignature = requestBlockSignature,
                    responsePayloadHash = result.responsePayloadHash!!,
                )
                val streamProof = result.executorProof!!
                val streamProofValid = verifySecp256k1Signature(
                    signingPayload = streamSigningPayload,
                    pubKeyBytes = Base64.getDecoder().decode(streamProof.executorSignerPubKey),
                    signatureBytes = Base64.getDecoder().decode(streamProof.executorSignature),
                )
                assertThat(streamProofValid)
                    .withFailMessage(
                        "stream proof verify failed participant=%s executor=%s signer=%s response_payload_hash=%s signing_payload_hex=%s signer_pubkey_prefix=%s signature_prefix=%s",
                        result.participantAddress,
                        streamProof.executorAddress,
                        streamProof.executorSignerAddress,
                        result.responsePayloadHash,
                        streamSigningPayload.joinToString("") { byte -> "%02x".format(byte.toInt() and 0xFF) },
                        streamProof.executorSignerPubKey.take(16),
                        streamProof.executorSignature.take(16),
                    )
                    .isTrue()
            }

            val firstResult = results.first()
            val firstProof = firstResult.executorProof!!
            val firstHash = firstResult.responsePayloadHash!!
            planController.recordFinishInference(
                requestSequence = sequence,
                openAiResponse = null,
                responsePayloadHash = firstHash,
                executorAddress = firstProof.executorAddress,
                executorSignerAddress = firstProof.executorSignerAddress,
                executorSignerPubKey = firstProof.executorSignerPubKey,
                executorSignature = firstProof.executorSignature,
                timestampSeconds = System.currentTimeMillis() / 1000,
            )
            val nextOpenAiRequest = buildOpenAiRequest(sequence = sequence + 1, stream = false)
            val nextPlan = planController.reserveStartInference(
                requestPayloadHash = computeV2RequestPayloadHash(nextOpenAiRequest),
                timestampSeconds = System.currentTimeMillis() / 1000,
            )
            val finishRequestId = buildV2RequestId(escrowId, sequence)
            val persistedFinish = nextPlan.developerChainDelta.blocks
                .flatMap { it.messages }
                .firstOrNull { it.type == FINISH_INFERENCE_MESSAGE_TYPE && it.requestId == finishRequestId }
            assertThat(persistedFinish).isNotNull()
            assertThat(persistedFinish?.responsePayloadHash).isEqualTo(firstHash)
            assertThat(persistedFinish?.executorAddress).isEqualTo(firstProof.executorAddress)
            assertThat(persistedFinish?.executorSignerAddress).isEqualTo(firstProof.executorSignerAddress)
            assertThat(persistedFinish?.executorSignerPubKey).isEqualTo(firstProof.executorSignerPubKey)
            assertThat(persistedFinish?.executorSignature).isEqualTo(firstProof.executorSignature)

            val normalizedStreams = results.map { it.dataLines }
            assertThat(normalizedStreams.distinct()).hasSize(1)
        } finally {
            pool.shutdownNow()
        }
    }

    @Test
    fun `developer gets streamed response when sending through non intended responsible participant`() {
        logSection("V2 relay streaming test setup")
        val fixture = setupV2EscrowFixture()
        val allPairs = fixture.allPairs
        val pairByAddress = fixture.pairByAddress
        val weightedParticipants = fixture.weightedParticipants
        val developerAddress = fixture.developerAddress
        val developerBlockSigner = fixture.developerBlockSigner
        val genesis = fixture.genesis
        val escrowContext = createEscrowAndWaitForIndexing(genesis, allPairs, developerAddress)
        val escrowId = escrowContext.escrowId
        val epochId = escrowContext.epochId

        val sequence = 1L
        val responsibleParticipants = selectResponsibleParticipantsDeterministic(
            participants = weightedParticipants,
            selectionCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
            escrowId = escrowId,
            sequence = sequence,
        )
        assertThat(responsibleParticipants).hasSize(3)
        val nonIntendedParticipant = responsibleParticipants[1]
        val nonIntendedPair = requireNotNull(pairByAddress[nonIntendedParticipant]) {
            "Missing pair for non-intended participant=$nonIntendedParticipant"
        }

        val openAiRequest = buildOpenAiRequest(sequence = sequence, stream = true)
        val request = buildSingleStartEnvelope(
            openAiRequest = openAiRequest,
            escrowId = escrowId,
            sequence = sequence,
            developerBlockSigner = developerBlockSigner,
        )

        val streamConnection = openV2Stream(
            pair = nonIntendedPair,
            request = request,
            requesterAddress = developerAddress,
            escrowId = escrowId,
            sequence = sequence,
            epochId = epochId,
        )
        try {
            assertThat(streamConnection.latestBlockSequence).isEqualTo(sequence)
            val firstLine = streamConnection.streamConnection.readLine()
            assertThat(firstLine).isNotNull
            assertThat(firstLine).contains("data:")
        } finally {
            streamConnection.streamConnection.close()
        }
    }

    @Test
    fun `leader disconnect mid-stream and follower still receives replay`() {
        logSection("V2 step13 streaming disconnect resilience test setup")
        val fixture = setupV2EscrowFixture()
        val allPairs = fixture.allPairs
        val genesis = fixture.genesis
        val pairByAddress = fixture.pairByAddress
        val weightedParticipants = fixture.weightedParticipants
        val developerAddress = fixture.developerAddress
        val developerBlockSigner = fixture.developerBlockSigner
        allPairs.forEach { pair ->
            pair.mock?.setInferenceResponse(
                openAIResponse = defaultInferenceResponseObject,
                delay = Duration.ofSeconds(3),
                streamDelay = Duration.ofMillis(40),
                model = defaultModel,
            )
        }

        val escrowContext = createEscrowAndWaitForIndexing(genesis, allPairs, developerAddress)
        val escrowId = escrowContext.escrowId
        val epochId = escrowContext.epochId

        val sequence = 1L
        val responsibleParticipants = selectResponsibleParticipantsDeterministic(
            participants = weightedParticipants,
            selectionCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
            escrowId = escrowId,
            sequence = sequence,
        )
        val intendedExecutor = responsibleParticipants.first()
        val intendedPair = requireNotNull(pairByAddress[intendedExecutor]) {
            "Missing pair for intended executor=$intendedExecutor"
        }
        val followerPair = requireNotNull(pairByAddress[responsibleParticipants[1]]) {
            "Missing pair for follower participant=${responsibleParticipants[1]}"
        }

        val openAiRequest = buildOpenAiRequest(sequence = sequence, stream = true)
        val request = buildSingleStartEnvelope(
            openAiRequest = openAiRequest,
            escrowId = escrowId,
            sequence = sequence,
            developerBlockSigner = developerBlockSigner,
        )

        // Leader opens stream and disconnects early.
        val leaderConnection = openV2Stream(
            pair = intendedPair,
            request = request,
            requesterAddress = developerAddress,
            escrowId = escrowId,
            sequence = sequence,
            epochId = epochId,
        )
        assertThat(leaderConnection.latestBlockSequence).isEqualTo(sequence)
        leaderConnection.streamConnection.readLine() // consume one line to ensure stream started
        leaderConnection.streamConnection.close()

        // Follower should still receive replay/history for same request identity.
        val followerConnection = openV2Stream(
            pair = followerPair,
            request = request,
            requesterAddress = developerAddress,
            escrowId = escrowId,
            sequence = sequence,
            epochId = epochId,
        )
        try {
            val replayData = readSSEDataLines(followerConnection.streamConnection)
            assertThat(replayData).isNotEmpty()
            assertThat(followerConnection.latestBlockSequence).isEqualTo(sequence)
        } finally {
            followerConnection.streamConnection.close()
        }
    }

    @Test
    fun `leader timeout on non-streaming and follower still gets completed replay`() {
        logSection("V2 step13 non-streaming disconnect resilience test setup")
        val fixture = setupV2EscrowFixture()
        val allPairs = fixture.allPairs
        val pairByAddress = fixture.pairByAddress
        val weightedParticipants = fixture.weightedParticipants
        val developerAddress = fixture.developerAddress
        val developerBlockSigner = fixture.developerBlockSigner
        val genesis = fixture.genesis
        val escrowContext = createEscrowAndWaitForIndexing(genesis, allPairs, developerAddress)
        val escrowId = escrowContext.escrowId
        val epochId = escrowContext.epochId

        val sequence = 1L
        val responsibleParticipants = selectResponsibleParticipantsDeterministic(
            participants = weightedParticipants,
            selectionCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
            escrowId = escrowId,
            sequence = sequence,
        )
        val intendedExecutor = responsibleParticipants.first()
        val intendedPair = requireNotNull(pairByAddress[intendedExecutor]) {
            "Missing pair for intended executor=$intendedExecutor"
        }
        val followerPair = requireNotNull(pairByAddress[responsibleParticipants[1]]) {
            "Missing pair for follower participant=${responsibleParticipants[1]}"
        }
        intendedPair.mock?.setInferenceResponse(
            openAIResponse = defaultInferenceResponseObject,
            delay = Duration.ofSeconds(3),
            streamDelay = Duration.ofMillis(50),
            model = defaultModel,
        )

        val openAiRequest = buildOpenAiRequest(sequence = sequence, stream = false)
        val request = buildSingleStartEnvelope(
            openAiRequest = openAiRequest,
            escrowId = escrowId,
            sequence = sequence,
            developerBlockSigner = developerBlockSigner,
        )

        assertThatThrownBy {
            makeV2RequestWithShortReadTimeout(
                publicURL = intendedPair.api.getPublicUrl(),
                request = request,
                requesterAddress = developerAddress,
                escrowId = escrowId,
                sequence = sequence,
                epochId = epochId,
                readTimeoutMs = 150,
            )
        }.isInstanceOf(SocketTimeoutException::class.java)

        val followerResponse = sendV2Request(
            pair = followerPair,
            request = request,
            requesterAddress = developerAddress,
            escrowId = escrowId,
            sequence = sequence,
            epochId = epochId,
        )
        assertThat(followerResponse.latestBlockSequence).isEqualTo(sequence)
        assertThat(followerResponse.openAIResponse.id).isNotBlank()
    }

    @Test
    fun `relay participant ingests chain and rejects overlap mismatch before forwarding`() {
        logSection("V2 relay-side chain ingestion test setup")
        val fixture = setupV2EscrowFixture()
        val allPairs = fixture.allPairs
        val genesis = fixture.genesis
        val pairByAddress = fixture.pairByAddress
        val weightedParticipants = fixture.weightedParticipants
        val developerAddress = fixture.developerAddress
        val developerBlockSigner = fixture.developerBlockSigner
        val escrowContext = createEscrowAndWaitForIndexing(genesis, allPairs, developerAddress)
        val escrowId = escrowContext.escrowId
        val epochId = escrowContext.epochId

        val sequence1 = 1L
        val responsibleForSeq1 = selectResponsibleParticipantsDeterministic(
            participants = weightedParticipants,
            selectionCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
            escrowId = escrowId,
            sequence = sequence1,
        )
        val relayParticipant = responsibleForSeq1[1]
        val relayPair = requireNotNull(pairByAddress[relayParticipant]) {
            "Missing pair for relay participant=$relayParticipant"
        }

        val request1 = buildOpenAiRequest(sequence = sequence1, stream = false)
        val request1Envelope = buildSingleStartEnvelope(
            openAiRequest = request1,
            escrowId = escrowId,
            sequence = sequence1,
            developerBlockSigner = developerBlockSigner,
        )
        val response1 = sendV2Request(
            pair = relayPair,
            request = request1Envelope,
            requesterAddress = developerAddress,
            escrowId = escrowId,
            sequence = sequence1,
            epochId = epochId,
        )
        assertThat(response1.latestBlockSequence).isEqualTo(sequence1)

        val sequence2 = 2L
        val request2 = buildOpenAiRequest(sequence = sequence2, stream = false)
        val request2Envelope = buildSingleStartEnvelope(
            openAiRequest = request2,
            escrowId = escrowId,
            sequence = sequence2,
            developerBlockSigner = developerBlockSigner,
        )
        val response2 = sendV2Request(
            pair = relayPair,
            request = request2Envelope,
            requesterAddress = developerAddress,
            escrowId = escrowId,
            sequence = sequence2,
            epochId = epochId,
        )
        assertThat(response2.latestBlockSequence).isEqualTo(sequence2)

        val sequence3 = 3L
        val request3 = buildOpenAiRequest(sequence = sequence3, stream = false)
        val mismatchedOverlapBlock2 = buildStartInferenceBlock(
            blockSequence = 2,
            escrowId = escrowId,
            modelId = defaultModel,
            requestPayloadHash = "deadbeef", // Deliberately mismatched overlap block content.
            timestampSeconds = (System.currentTimeMillis() / 1000) + 77,
            developerBlockSigner = developerBlockSigner,
        )
        val request3Envelope = buildV2RequestEnvelope(
            openAiRequest = request3,
            developerChainDelta = DeveloperChainDelta(
                baseBlockSequence = 1,
                blocks = listOf(
                    mismatchedOverlapBlock2,
                    buildStartInferenceBlock(
                        blockSequence = sequence3,
                        escrowId = escrowId,
                        modelId = defaultModel,
                        requestPayloadHash = computeV2RequestPayloadHash(request3),
                        timestampSeconds = System.currentTimeMillis() / 1000,
                        developerBlockSigner = developerBlockSigner,
                    )
                ),
                latestBlockSequence = sequence3,
            )
        )

        assertThatThrownBy {
            sendV2Request(
                pair = relayPair,
                request = request3Envelope,
                requesterAddress = developerAddress,
                escrowId = escrowId,
                sequence = sequence3,
                epochId = epochId,
            )
        }.hasMessageContaining("409")
    }

    @Test
    fun `relay failures return signed artifacts from distinct relays`() {
        logSection("V2 Step16 relay signed-error quorum test setup")
        val fixture = setupV2EscrowFixture()
        val allPairs = fixture.allPairs
        val genesis = fixture.genesis
        val pairByAddress = fixture.pairByAddress
        val weightedParticipants = fixture.weightedParticipants
        val developerAddress = fixture.developerAddress
        val developerBlockSigner = fixture.developerBlockSigner
        val escrowContext = createEscrowAndWaitForIndexing(genesis, allPairs, developerAddress)
        val escrowId = escrowContext.escrowId
        val epochId = escrowContext.epochId

        val sequence1 = 1L
        val responsibleForSeq1 = selectResponsibleParticipantsDeterministic(
            participants = weightedParticipants,
            selectionCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
            escrowId = escrowId,
            sequence = sequence1,
        )
        assertThat(responsibleForSeq1).hasSize(3)
        val intendedExecutorAddress = responsibleForSeq1.first()
        val relayAddressA = responsibleForSeq1[1]
        val relayAddressB = responsibleForSeq1[2]

        val intendedPair = requireNotNull(pairByAddress[intendedExecutorAddress]) {
            "Missing pair for intended executor=$intendedExecutorAddress"
        }
        val relayPairA = requireNotNull(pairByAddress[relayAddressA]) {
            "Missing pair for relay participant=$relayAddressA"
        }
        val relayPairB = requireNotNull(pairByAddress[relayAddressB]) {
            "Missing pair for relay participant=$relayAddressB"
        }

        val openAiRequest1 = buildOpenAiRequest(sequence = sequence1, stream = false)
        val request1EnvelopeJson = buildSingleStartEnvelope(
            openAiRequest = openAiRequest1,
            escrowId = escrowId,
            sequence = sequence1,
            developerBlockSigner = developerBlockSigner,
        )
        val requestId1 = buildV2RequestId(escrowId, sequence1)

        setApiContainerRunning(intendedPair, running = false)
        try {
            val artifacts = collectRelayErrorArtifactsForRequest(
                request = request1EnvelopeJson,
                requesterAddress = developerAddress,
                escrowId = escrowId,
                sequence = sequence1,
                epochId = epochId,
                responsibleParticipants = responsibleForSeq1,
                pairByAddress = pairByAddress,
            )
            assertThat(artifacts).hasSize(2)
            val artifactA = artifacts.first { it.relayAddress == relayAddressA }
            val artifactB = artifacts.first { it.relayAddress == relayAddressB }

            assertThat(artifactA.requestId).isEqualTo(requestId1)
            assertThat(artifactB.requestId).isEqualTo(requestId1)
            assertThat(artifactA.escrowId).isEqualTo(escrowId)
            assertThat(artifactB.escrowId).isEqualTo(escrowId)
            assertThat(artifactA.intendedExecutorAddress).isEqualTo(intendedExecutorAddress)
            assertThat(artifactB.intendedExecutorAddress).isEqualTo(intendedExecutorAddress)
            assertThat(artifactA.relayAddress).isEqualTo(relayAddressA)
            assertThat(artifactB.relayAddress).isEqualTo(relayAddressB)
            assertThat(artifactA.relaySignature).isNotBlank()
            assertThat(artifactB.relaySignature).isNotBlank()
            assertThat(verifyV2RelayErrorArtifactSignature(artifactA)).isTrue()
            assertThat(verifyV2RelayErrorArtifactSignature(artifactB)).isTrue()
            assertThat(setOf(artifactA.relayAddress, artifactB.relayAddress)).hasSize(2)
            assertThat(artifactA.failureCode).isNotBlank()
            assertThat(artifactB.failureCode).isNotBlank()
        } finally {
            setApiContainerRunning(intendedPair, running = true)
        }
    }

    @Test
    fun `state hash converges across responsible participants and mismatched signed state hash is rejected`() {
        logSection("V2 Step17 deterministic state hash convergence test setup")
        val fixture = setupV2EscrowFixture()
        val allPairs = fixture.allPairs
        val genesis = fixture.genesis
        val pairByAddress = fixture.pairByAddress
        val weightedParticipants = fixture.weightedParticipants
        val developerAddress = fixture.developerAddress
        val developerBlockSigner = fixture.developerBlockSigner
        val escrowContext = createEscrowAndWaitForIndexing(genesis, allPairs, developerAddress)
        val escrowId = escrowContext.escrowId
        val epochId = escrowContext.epochId

        val sequence1 = 1L
        val request1 = buildOpenAiRequest(sequence = sequence1, stream = false)
        val request1Envelope = buildSingleStartEnvelope(
            openAiRequest = request1,
            escrowId = escrowId,
            sequence = sequence1,
            developerBlockSigner = developerBlockSigner,
        )
        val request1Delta = cosmosJson.fromJson(request1Envelope, DeveloperChainEnvelope::class.java).developerChainDelta
        val block1 = request1Delta.blocks.single()
        val responsibleForSeq1 = selectResponsibleParticipantsDeterministic(
            participants = weightedParticipants,
            selectionCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
            escrowId = escrowId,
            sequence = sequence1,
        )
        val intendedSeq1 = requireNotNull(pairByAddress[responsibleForSeq1.first()]) {
            "Missing pair for sequence=$sequence1"
        }
        val response1 = sendV2Request(
            pair = intendedSeq1,
            request = request1Envelope,
            requesterAddress = developerAddress,
            escrowId = escrowId,
            sequence = sequence1,
            epochId = epochId,
        )
        assertThat(response1.latestBlockSequence).isEqualTo(sequence1)

        val stateAfterBlock1 = applyDeterministicState(DeterministicChainState(), block1.messages)
        val sequence2 = 2L
        val request2 = buildOpenAiRequest(sequence = sequence2, stream = false)
        val block2Messages = listOf(
            DeveloperChainMessage(
                type = FINISH_INFERENCE_MESSAGE_TYPE,
                requestId = buildV2RequestId(escrowId, sequence1),
                status = "finished",
                responsePayloadHash = response1.responsePayloadHash,
                executorAddress = response1.executorAddress,
                executorSignerAddress = response1.executorSignerAddress,
                executorSignerPubKey = response1.executorSignerPubKey,
                executorSignature = response1.executorSignature,
                inputTokenCount = extractInputTokens(response1.openAIResponse),
                outputTokenCount = extractOutputTokens(response1.openAIResponse),
                timestamp = System.currentTimeMillis() / 1000,
            ),
            DeveloperChainMessage(
                type = START_INFERENCE_MESSAGE_TYPE,
                requestId = buildV2RequestId(escrowId, sequence2),
                modelId = defaultModel,
                requestPayloadHash = computeV2RequestPayloadHash(request2),
                timestamp = System.currentTimeMillis() / 1000,
            ),
        )
        val signedBlock2 = signDeveloperChainBlockWithState(
            blockSequence = sequence2,
            escrowId = escrowId,
            messages = block2Messages,
            developerBlockSigner = developerBlockSigner,
            baseState = stateAfterBlock1,
        )
        val request2Envelope = buildV2RequestEnvelope(
            openAiRequest = request2,
            developerChainDelta = DeveloperChainDelta(
                baseBlockSequence = 0,
                blocks = listOf(block1, signedBlock2.block),
                latestBlockSequence = sequence2,
            ),
        )
        val responsibleForSeq2 = selectResponsibleParticipantsDeterministic(
            participants = weightedParticipants,
            selectionCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
            escrowId = escrowId,
            sequence = sequence2,
        )
        // Every responsible participant should converge on the same signed state and accept overlap.
        responsibleForSeq2.forEach { participantAddress ->
            val pair = requireNotNull(pairByAddress[participantAddress]) {
                "Missing pair for participant=$participantAddress"
            }
            val response = sendV2Request(
                pair = pair,
                request = request2Envelope,
                requesterAddress = developerAddress,
                escrowId = escrowId,
                sequence = sequence2,
                epochId = epochId,
            )
            assertThat(response.latestBlockSequence).isEqualTo(sequence2)
        }

        val stateAfterBlock2 = signedBlock2.nextState
        val sequence3 = 3L
        val request3 = buildOpenAiRequest(sequence = sequence3, stream = false)
        val block3Messages = listOf(
            DeveloperChainMessage(
                type = START_INFERENCE_MESSAGE_TYPE,
                requestId = buildV2RequestId(escrowId, sequence3),
                modelId = defaultModel,
                requestPayloadHash = computeV2RequestPayloadHash(request3),
                timestamp = System.currentTimeMillis() / 1000,
            )
        )
        val tamperedBlock3 = signDeveloperChainBlockWithExplicitStateHash(
            blockSequence = sequence3,
            escrowId = escrowId,
            messages = block3Messages,
            developerBlockSigner = developerBlockSigner,
            stateHash = "deadbeef",
        )
        val request3Envelope = buildV2RequestEnvelope(
            openAiRequest = request3,
            developerChainDelta = DeveloperChainDelta(
                baseBlockSequence = 2,
                blocks = listOf(tamperedBlock3),
                latestBlockSequence = sequence3,
            ),
        )
        val intendedForSeq3 = selectResponsibleParticipantsDeterministic(
            participants = weightedParticipants,
            selectionCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
            escrowId = escrowId,
            sequence = sequence3,
        ).first()
        val intendedSeq3 = requireNotNull(pairByAddress[intendedForSeq3]) {
            "Missing pair for sequence=$sequence3"
        }
        assertThat(stateAfterBlock2.executorStats).isNotEmpty()
        assertThatThrownBy {
            sendV2Request(
                pair = intendedSeq3,
                request = request3Envelope,
                requesterAddress = developerAddress,
                escrowId = escrowId,
                sequence = sequence3,
                epochId = epochId,
            )
        }.hasMessageContaining("409")
    }

    @Test
    fun `conflicting overlap invalidates escrow and all participants reject subsequent requests`() {
        logSection("V2 Step15 escrow invalidation E2E setup")
        val fixture = setupV2EscrowFixture()
        val allPairs = fixture.allPairs
        val genesis = fixture.genesis
        val pairByAddress = fixture.pairByAddress
        val weightedParticipants = fixture.weightedParticipants
        val developerAddress = fixture.developerAddress
        val developerBlockSigner = fixture.developerBlockSigner
        val escrowContext = createEscrowAndWaitForIndexing(genesis, allPairs, developerAddress)
        val escrowId = escrowContext.escrowId
        val epochId = escrowContext.epochId

        val intendedForSeq3 = selectResponsibleParticipantsDeterministic(
            participants = weightedParticipants,
            selectionCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
            escrowId = escrowId,
            sequence = 3L,
        ).first()
        val intendedPair = requireNotNull(pairByAddress[intendedForSeq3]) {
            "Missing pair for intended participant=$intendedForSeq3"
        }

        val request1 = buildOpenAiRequest(sequence = 1L, stream = false)
        val block1 = buildStartInferenceBlock(
            blockSequence = 1L,
            escrowId = escrowId,
            modelId = defaultModel,
            requestPayloadHash = computeV2RequestPayloadHash(request1),
            timestampSeconds = System.currentTimeMillis() / 1000,
            developerBlockSigner = developerBlockSigner,
        )
        val request1Envelope = buildV2RequestEnvelope(
            openAiRequest = request1,
            developerChainDelta = DeveloperChainDelta(
                baseBlockSequence = 0,
                blocks = listOf(block1),
                latestBlockSequence = 1,
            )
        )
        val response1 = sendV2Request(
            pair = intendedPair,
            request = request1Envelope,
            requesterAddress = developerAddress,
            escrowId = escrowId,
            sequence = 1L,
            epochId = epochId,
        )
        assertThat(response1.latestBlockSequence).isEqualTo(1L)

        val request2 = buildOpenAiRequest(sequence = 2L, stream = false)
        val block2 = buildStartInferenceBlock(
            blockSequence = 2L,
            escrowId = escrowId,
            modelId = defaultModel,
            requestPayloadHash = computeV2RequestPayloadHash(request2),
            timestampSeconds = System.currentTimeMillis() / 1000,
            developerBlockSigner = developerBlockSigner,
        )
        val request2Envelope = buildV2RequestEnvelope(
            openAiRequest = request2,
            developerChainDelta = DeveloperChainDelta(
                baseBlockSequence = 0,
                blocks = listOf(block1, block2),
                latestBlockSequence = 2,
            )
        )
        val response2 = sendV2Request(
            pair = intendedPair,
            request = request2Envelope,
            requesterAddress = developerAddress,
            escrowId = escrowId,
            sequence = 2L,
            epochId = epochId,
        )
        assertThat(response2.latestBlockSequence).isEqualTo(2L)

        val request3 = buildOpenAiRequest(sequence = 3L, stream = false)
        val mismatchedOverlapBlock2 = buildStartInferenceBlock(
            blockSequence = 2L,
            escrowId = escrowId,
            modelId = defaultModel,
            requestPayloadHash = "deadbeef",
            timestampSeconds = (System.currentTimeMillis() / 1000) + 77,
            developerBlockSigner = developerBlockSigner,
        )
        val request3Envelope = buildV2RequestEnvelope(
            openAiRequest = request3,
            developerChainDelta = DeveloperChainDelta(
                baseBlockSequence = 1,
                blocks = listOf(
                    mismatchedOverlapBlock2,
                    buildStartInferenceBlock(
                        blockSequence = 3L,
                        escrowId = escrowId,
                        modelId = defaultModel,
                        requestPayloadHash = computeV2RequestPayloadHash(request3),
                        timestampSeconds = System.currentTimeMillis() / 1000,
                        developerBlockSigner = developerBlockSigner,
                    )
                ),
                latestBlockSequence = 3,
            )
        )
        assertThatThrownBy {
            sendV2Request(
                pair = intendedPair,
                request = request3Envelope,
                requesterAddress = developerAddress,
                escrowId = escrowId,
                sequence = 3L,
                epochId = epochId,
            )
        }.hasMessageContaining("409")

        // Wait until all API nodes ingest the on-chain escrow_invalidated event.
        allPairs.forEach { it.node.waitForNextBlock(2) }

        val request4 = buildOpenAiRequest(sequence = 4L, stream = false)
        val request4Envelope = buildSingleStartEnvelope(
            openAiRequest = request4,
            escrowId = escrowId,
            sequence = 4L,
            developerBlockSigner = developerBlockSigner,
        )
        val responsibleForSeq4 = selectResponsibleParticipantsDeterministic(
            participants = weightedParticipants,
            selectionCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
            escrowId = escrowId,
            sequence = 4L,
        )
        responsibleForSeq4.forEach { participantAddress ->
            val pair = requireNotNull(pairByAddress[participantAddress]) {
                "Missing pair for participant=$participantAddress"
            }
            assertThatThrownBy {
                sendV2Request(
                    pair = pair,
                    request = request4Envelope,
                    requesterAddress = developerAddress,
                    escrowId = escrowId,
                    sequence = 4L,
                    epochId = epochId,
                )
            }.hasMessageContaining("409")
        }
    }

    private fun setupV2EscrowFixture(joinCount: Int = 2, reboot: Boolean = false): V2EscrowFixture {
        val (cluster, genesis) = initCluster(joinCount = joinCount, reboot = reboot)
        val allPairs = listOf(genesis) + cluster.joinPairs
        assertThat(allPairs).hasSize(joinCount + 1)

        allPairs.forEach { it.waitForMlNodesToLoad() }
        genesis.waitForNextInferenceWindow()

        val developerAddress = genesis.node.getColdAddress()
        val developerBlockSigner = createDeveloperBlockSigner(genesis, developerAddress)
        val pairByAddress = allPairs.associateBy { it.node.getColdAddress() }
        val (weightedParticipants, participantPubKeyByAddress) = resolveEligibleParticipantMetadata(genesis, pairByAddress.keys)
        assertThat(weightedParticipants).hasSize(joinCount + 1)

        return V2EscrowFixture(
            genesis = genesis,
            allPairs = allPairs,
            developerAddress = developerAddress,
            developerBlockSigner = developerBlockSigner,
            pairByAddress = pairByAddress,
            weightedParticipants = weightedParticipants,
            participantPubKeyByAddress = participantPubKeyByAddress,
        )
    }

    private fun buildSingleStartEnvelope(
        openAiRequest: InferenceRequestPayload,
        escrowId: String,
        sequence: Long,
        developerBlockSigner: DeveloperBlockSigner,
        baseBlockSequence: Long = sequence - 1,
        timestampSeconds: Long = System.currentTimeMillis() / 1000,
        requestPayloadHashOverride: String? = null,
    ): String {
        val block = buildStartInferenceBlock(
            blockSequence = sequence,
            escrowId = escrowId,
            modelId = defaultModel,
            requestPayloadHash = requestPayloadHashOverride ?: computeV2RequestPayloadHash(openAiRequest),
            timestampSeconds = timestampSeconds,
            developerBlockSigner = developerBlockSigner,
        )
        return buildV2RequestEnvelope(
            openAiRequest = openAiRequest,
            developerChainDelta = DeveloperChainDelta(
                baseBlockSequence = baseBlockSequence,
                blocks = listOf(block),
                latestBlockSequence = sequence,
            )
        )
    }

    private fun sendV2Request(
        pair: LocalInferencePair,
        request: String,
        requesterAddress: String,
        escrowId: String,
        sequence: Long,
        epochId: Long,
    ): V2InferenceResponse {
        return pair.api.makeInferenceRequestV2(
            request = request,
            requesterAddress = requesterAddress,
            escrowId = escrowId,
            sequence = sequence,
            epochId = epochId,
        )
    }

    private fun openV2Stream(
        pair: LocalInferencePair,
        request: String,
        requesterAddress: String,
        escrowId: String,
        sequence: Long,
        epochId: Long,
    ): V2InferenceStreamConnection {
        return pair.api.createInferenceStreamConnectionV2(
            request = request,
            requesterAddress = requesterAddress,
            escrowId = escrowId,
            sequence = sequence,
            epochId = epochId,
        )
    }

    private fun resolveEligibleParticipantMetadata(
        genesis: LocalInferencePair,
        expectedAddresses: Set<String>,
    ): Pair<List<WeightedParticipant>, Map<String, String>> {
        val activeParticipants = genesis.api.getActiveParticipants()
            .activeParticipants
            .participants
            .associateBy { it.index }

        val weightedParticipants = expectedAddresses.sorted().mapNotNull { address ->
            val participant = activeParticipants[address] ?: return@mapNotNull null
            if (!participant.models.contains(defaultModel) || participant.weight <= 0) {
                return@mapNotNull null
            }
            WeightedParticipant(address = address, weight = participant.weight.toULong())
        }
        val participantPubKeyByAddress = expectedAddresses.sorted().mapNotNull { address ->
            val participant = activeParticipants[address] ?: return@mapNotNull null
            if (participant.validatorKey.isBlank()) {
                return@mapNotNull null
            }
            address to participant.validatorKey
        }.toMap()
        return weightedParticipants to participantPubKeyByAddress
    }

    private fun extractEscrowId(response: TxResponse): String? {
        return response.events
            .firstOrNull { it.type == "escrow_created" }
            ?.attributes
            ?.firstOrNull { attribute ->
                attribute.key == "escrow_id" || attribute.key.endsWith(".escrow_id")
            }
            ?.value
    }

    private fun extractEscrowEpochId(response: TxResponse): Long? {
        return response.events
            .firstOrNull { it.type == "escrow_created" }
            ?.attributes
            ?.firstOrNull { attribute ->
                attribute.key == "epoch_id" || attribute.key.endsWith(".epoch_id")
            }
            ?.value
            ?.toLongOrNull()
    }

    private fun extractEscrowContext(response: TxResponse): EscrowContext? {
        val escrowId = extractEscrowId(response) ?: return null
        val epochId = extractEscrowEpochId(response) ?: return null
        return EscrowContext(escrowId = escrowId, epochId = epochId)
    }

    private fun buildV2RequestEnvelope(
        openAiRequest: InferenceRequestPayload,
        developerChainDelta: DeveloperChainDelta,
    ): String {
        val envelope = DeveloperChainEnvelope(
            openAiRequest = openAiRequest,
            developerChainDelta = developerChainDelta,
        )
        return cosmosJson.toJson(envelope)
    }

    private fun buildV2RequestId(escrowId: String, sequence: Long): String {
        return "$escrowId:$sequence"
    }

    private fun createDeveloperBlockSigner(
        signerPair: LocalInferencePair,
        signerAddress: String,
    ): DeveloperBlockSigner {
        val runtimeChainId = signerPair.node.getStatus().nodeInfo.network
        return DeveloperBlockSigner(
            signingAccountAddress = signerAddress,
            chainId = if (runtimeChainId.isBlank()) inferenceConfig.chainId else runtimeChainId,
            signPayloadHex = { payloadHashHex ->
                signerPair.node.signPayload(
                    payload = payloadHashHex,
                    accountAddress = signerAddress,
                )
            },
        )
    }

    private fun buildStartInferenceBlock(
        blockSequence: Long,
        escrowId: String,
        modelId: String,
        requestPayloadHash: String,
        timestampSeconds: Long,
        developerBlockSigner: DeveloperBlockSigner,
    ): DeveloperChainBlock {
        val messages = listOf(
            DeveloperChainMessage(
                type = START_INFERENCE_MESSAGE_TYPE,
                requestId = buildV2RequestId(escrowId, blockSequence),
                modelId = modelId,
                requestPayloadHash = requestPayloadHash,
                timestamp = timestampSeconds,
            )
        )
        return signDeveloperChainBlock(
            blockSequence = blockSequence,
            escrowId = escrowId,
            messages = messages,
            developerBlockSigner = developerBlockSigner,
        )
    }

    private fun createEscrowAndWaitForIndexing(
        genesis: LocalInferencePair,
        allPairs: List<LocalInferencePair>,
        developerAddress: String,
    ): EscrowContext {
        val createEscrowTx = genesis.submitMessage(
            MsgCreateEscrow(
                creator = developerAddress,
                modelId = defaultModel,
            )
        )
        assertThat(createEscrowTx.code).isEqualTo(0)
        val escrowContext = requireNotNull(extractEscrowContext(createEscrowTx)) {
            "Could not extract escrow context from tx events: ${createEscrowTx.events}"
        }
        val indexedHeightBarrier = createEscrowTx.height + 1
        allPairs.forEach { pair ->
            pair.node.waitForMinimumBlock(indexedHeightBarrier, "escrow indexing barrier")
        }
        return escrowContext
    }

    private fun readSSEDataLines(streamConnection: com.productscience.StreamConnection): List<String> {
        return readV2StreamDataAndProof(streamConnection).dataLines
    }

    private fun readV2StreamDataAndProof(streamConnection: com.productscience.StreamConnection): V2StreamReadResult {
        val dataLines = mutableListOf<String>()
        val hashedStream = ByteArrayOutputStream()
        var currentEvent = "message"
        var executorProof: V2ExecutorProof? = null
        repeat(400) {
            val line = streamConnection.readLine() ?: return V2StreamReadResult(
                dataLines = dataLines,
                executorProof = executorProof,
                responsePayloadHash = if (hashedStream.size() > 0) sha256Hex(hashedStream.toByteArray()) else null,
            )
            val trimmed = line.trim()
            if (trimmed.startsWith("event:")) {
                currentEvent = trimmed.removePrefix("event:").trim()
                if (currentEvent != V2_EXECUTOR_PROOF_EVENT) {
                    hashedStream.write((line + "\n").toByteArray(Charsets.UTF_8))
                }
                return@repeat
            }
            if (trimmed.startsWith("data:")) {
                val payload = trimmed.removePrefix("data:").trimStart()
                if (currentEvent == V2_EXECUTOR_PROOF_EVENT) {
                    executorProof = runCatching {
                        cosmosJson.fromJson(payload, V2ExecutorProof::class.java)
                    }.getOrNull()
                    return@repeat
                }
                hashedStream.write((line + "\n").toByteArray(Charsets.UTF_8))
                if (payload == "[DONE]") {
                    return V2StreamReadResult(
                        dataLines = dataLines,
                        executorProof = executorProof,
                        responsePayloadHash = sha256Hex(hashedStream.toByteArray()),
                    )
                }
                dataLines += trimmed
                return@repeat
            }
            if (currentEvent != V2_EXECUTOR_PROOF_EVENT) {
                hashedStream.write((line + "\n").toByteArray(Charsets.UTF_8))
            }
            if (trimmed.isEmpty()) {
                currentEvent = "message"
            }
        }
        return V2StreamReadResult(
            dataLines = dataLines,
            executorProof = executorProof,
            responsePayloadHash = if (hashedStream.size() > 0) sha256Hex(hashedStream.toByteArray()) else null,
        )
    }

    private fun makeV2RequestWithShortReadTimeout(
        publicURL: String,
        request: String,
        requesterAddress: String,
        escrowId: String,
        sequence: Long,
        epochId: Long,
        readTimeoutMs: Int,
    ) {
        val endpoint = java.net.URI("$publicURL/v2/chat/completions").toURL()
        val connection = endpoint.openConnection() as HttpURLConnection
        connection.requestMethod = "POST"
        connection.connectTimeout = 1_000
        connection.readTimeout = readTimeoutMs
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
            // Triggers blocking read for response headers/body; should timeout under delayed executor.
            connection.responseCode
        } finally {
            connection.disconnect()
        }
    }

    private fun makeV2RequestRaw(
        publicURL: String,
        request: String,
        requesterAddress: String,
        escrowId: String,
        sequence: Long,
        epochId: Long,
    ): V2RawHttpResponse {
        val endpoint = java.net.URI("$publicURL/v2/chat/completions").toURL()
        val connection = endpoint.openConnection() as HttpURLConnection
        connection.requestMethod = "POST"
        connection.connectTimeout = 5_000
        connection.readTimeout = 30_000
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

        val statusCode = connection.responseCode
        val body = (if (statusCode in 200..299) connection.inputStream else connection.errorStream)
            ?.bufferedReader()
            ?.use { it.readText() }
            .orEmpty()
        connection.disconnect()
        return V2RawHttpResponse(statusCode = statusCode, body = body)
    }

    private fun collectRelayErrorArtifactsForRequest(
        request: String,
        requesterAddress: String,
        escrowId: String,
        sequence: Long,
        epochId: Long,
        responsibleParticipants: List<String>,
        pairByAddress: Map<String, LocalInferencePair>,
    ): List<V2RelayErrorArtifact> {
        val artifacts = mutableListOf<V2RelayErrorArtifact>()
        responsibleParticipants.drop(1).forEach { relayAddress ->
            val relayPair = requireNotNull(pairByAddress[relayAddress]) {
                "Missing relay pair for participant=$relayAddress"
            }
            val relayResponse = makeV2RequestRaw(
                publicURL = relayPair.api.getPublicUrl(),
                request = request,
                requesterAddress = requesterAddress,
                escrowId = escrowId,
                sequence = sequence,
                epochId = epochId,
            )
            if (relayResponse.statusCode == HttpURLConnection.HTTP_UNAVAILABLE) {
                artifacts += parseV2RelayErrorArtifact(relayResponse.body)
            }
        }
        return artifacts
    }

    private fun setApiContainerRunning(pair: LocalInferencePair, running: Boolean) {
        val dockerClient = DockerClientBuilder.getInstance().build()
        val containerName = pair.name.trimStart('/')
        val publicPort = java.net.URI(pair.api.getPublicUrl()).port
        val apiContainer = dockerClient.listContainersCmd().withShowAll(true).exec().firstOrNull { container ->
            container.ports.any { portMapping ->
                portMapping.privatePort == 9000 && portMapping.publicPort == publicPort
            }
        } ?: dockerClient.listContainersCmd().withShowAll(true).exec().firstOrNull { container ->
            container.names.any { name ->
                name == "$containerName-api" || name == "/$containerName-api"
            }
        }
        requireNotNull(apiContainer) {
            "API container not found for pair=${pair.name} publicPort=$publicPort"
        }
        if (running) {
            runCatching { dockerClient.startContainerCmd(apiContainer.id).exec() }
        } else {
            runCatching { dockerClient.stopContainerCmd(apiContainer.id).exec() }
        }
    }

    private inner class DeveloperChainController(
        private val escrowId: String,
        private val modelId: String,
        private val weightedParticipants: List<WeightedParticipant>,
        private val participantPubKeyByAddress: Map<String, String>,
        private val developerBlockSigner: DeveloperBlockSigner,
    ) {
        private val lock = ReentrantLock()
        private var latestReservedSequence = 0L
        private val chainBlocks = mutableListOf<DeveloperChainBlock>()
        private var deterministicState = DeterministicChainState()
        private val pendingFinishMessagesByRequestSequence = linkedMapOf<Long, DeveloperChainMessage>()
        private val pendingMissedMessagesByRequestSequence = linkedMapOf<Long, DeveloperChainMessage>()
        private val acknowledgedByRecipient = mutableMapOf<String, Long>()

        fun reserveStartInference(
            requestPayloadHash: String,
            timestampSeconds: Long,
        ): ReservedStartInferencePlan = lock.withLock {
            val sequence = latestReservedSequence + 1
            latestReservedSequence = sequence
            val requestId = buildV2RequestId(escrowId, sequence)
            val recipientAddress = selectResponsibleParticipantsDeterministic(
                participants = weightedParticipants,
                selectionCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
                escrowId = escrowId,
                sequence = sequence,
            ).first()

            val finishMessages = pendingFinishMessagesByRequestSequence
                .toSortedMap()
                .values
                .toList()
            val missedMessages = pendingMissedMessagesByRequestSequence
                .toSortedMap()
                .values
                .toList()
            pendingFinishMessagesByRequestSequence.clear()
            pendingMissedMessagesByRequestSequence.clear()

            val blockMessages = finishMessages + missedMessages + listOf(
                DeveloperChainMessage(
                    type = START_INFERENCE_MESSAGE_TYPE,
                    requestId = requestId,
                    modelId = modelId,
                    requestPayloadHash = requestPayloadHash,
                    timestamp = timestampSeconds,
                )
            )
            val signedBlock = signDeveloperChainBlockWithState(
                blockSequence = sequence,
                escrowId = escrowId,
                messages = blockMessages,
                developerBlockSigner = developerBlockSigner,
                baseState = deterministicState,
            )
            deterministicState = signedBlock.nextState
            chainBlocks += signedBlock.block

            val recipientAcked = acknowledgedByRecipient[recipientAddress] ?: 0L
            val deltaBlocks = chainBlocks.filter { it.blockSequence > recipientAcked }

            ReservedStartInferencePlan(
                sequence = sequence,
                recipientAddress = recipientAddress,
                developerChainDelta = DeveloperChainDelta(
                    baseBlockSequence = recipientAcked,
                    blocks = deltaBlocks,
                    latestBlockSequence = sequence,
                ),
            )
        }

        fun recordReceiverAcknowledgment(recipientAddress: String, latestBlockSequence: Long) = lock.withLock {
            val current = acknowledgedByRecipient[recipientAddress] ?: 0L
            if (latestBlockSequence > current) {
                acknowledgedByRecipient[recipientAddress] = latestBlockSequence
            }
        }

        fun getReceiverAcknowledgedBlockSequence(recipientAddress: String): Long = lock.withLock {
            acknowledgedByRecipient[recipientAddress] ?: 0L
        }

        fun recordFinishInference(
            requestSequence: Long,
            openAiResponse: Any?,
            responsePayloadHash: String,
            executorAddress: String?,
            executorSignerAddress: String?,
            executorSignerPubKey: String?,
            executorSignature: String?,
            timestampSeconds: Long,
            status: String = "finished",
        ) = lock.withLock {
            // Non-streaming executor proof is signed over SHA256(raw response bytes), not JSON re-serialization.
            // Keep only a non-empty guard here; cryptographic proof check is performed below.
            assertThat(responsePayloadHash).isNotBlank()
            assertThat(executorAddress).isNotBlank()
            assertThat(executorSignerAddress).isNotBlank()
            assertThat(executorSignerPubKey).isNotBlank()
            assertThat(executorSignature).isNotBlank()
            val requestBlockSignature = chainBlocks
                .firstOrNull { it.blockSequence == requestSequence }
                ?.signature
                ?: error("Missing request block signature for sequence=$requestSequence")
            verifyExecutorProof(
                requestBlockSignature = requestBlockSignature,
                responsePayloadHash = responsePayloadHash,
                executorAddress = executorAddress!!,
                executorSignerAddress = executorSignerAddress!!,
                executorSignerPubKey = executorSignerPubKey!!,
                executorSignature = executorSignature!!,
            )
            val requestId = buildV2RequestId(escrowId, requestSequence)
            pendingFinishMessagesByRequestSequence[requestSequence] = DeveloperChainMessage(
                type = FINISH_INFERENCE_MESSAGE_TYPE,
                requestId = requestId,
                status = status,
                responsePayloadHash = responsePayloadHash,
                executorAddress = executorAddress,
                executorSignerAddress = executorSignerAddress,
                executorSignerPubKey = executorSignerPubKey,
                executorSignature = executorSignature,
                inputTokenCount = extractInputTokens(openAiResponse),
                outputTokenCount = extractOutputTokens(openAiResponse),
                timestamp = timestampSeconds,
            )
        }

        fun recordMissedInference(
            requestSequence: Long,
            relayErrors: List<V2RelayErrorArtifact>,
            timestampSeconds: Long,
        ) = lock.withLock {
            require(relayErrors.isNotEmpty()) { "MissedInference relay_errors must not be empty" }
            val requestId = buildV2RequestId(escrowId, requestSequence)
            val evidence = cosmosJson.toJson(
                V2MissedInferenceEvidencePayload(relayErrors = relayErrors)
            )
            pendingFinishMessagesByRequestSequence.remove(requestSequence)
            pendingMissedMessagesByRequestSequence[requestSequence] = DeveloperChainMessage(
                type = MISSED_INFERENCE_MESSAGE_TYPE,
                requestId = requestId,
                missedInferenceEvidence = evidence,
                timestamp = timestampSeconds,
            )
        }

        private fun verifyExecutorProof(
            requestBlockSignature: String,
            responsePayloadHash: String,
            executorAddress: String,
            executorSignerAddress: String,
            executorSignerPubKey: String,
            executorSignature: String,
        ) {
            val signingPayload = buildExecutorProofSigningPayload(
                developerRequestBlockSignature = requestBlockSignature,
                responsePayloadHash = responsePayloadHash,
            )
            val pubKeyBytes = Base64.getDecoder().decode(executorSignerPubKey)
            val signatureBytes = Base64.getDecoder().decode(executorSignature)
            val signatureValid = verifySecp256k1Signature(signingPayload, pubKeyBytes, signatureBytes)
            assertThat(signatureValid).isTrue()
        }
    }

    private fun buildOpenAiRequest(sequence: Long, stream: Boolean): InferenceRequestPayload {
        return inferenceRequestObject.copy(
            seed = inferenceRequestObject.seed + sequence.toInt(),
            stream = stream,
        )
    }

    private fun computeV2RequestPayloadHash(openAiRequest: InferenceRequestPayload): String {
        val openAiRequestJson = cosmosJson.toJson(openAiRequest)
        val digest = MessageDigest.getInstance("SHA-256").digest(openAiRequestJson.toByteArray(Charsets.UTF_8))
        return digest.joinToString("") { byte -> "%02x".format(byte.toInt() and 0xFF) }
    }

    private fun computeV2ResponsePayloadHash(openAiResponse: Any): String {
        val openAiResponseJson = cosmosJson.toJson(openAiResponse)
        val digest = MessageDigest.getInstance("SHA-256").digest(openAiResponseJson.toByteArray(Charsets.UTF_8))
        return digest.joinToString("") { byte -> "%02x".format(byte.toInt() and 0xFF) }
    }

    private fun signDeveloperChainBlockWithState(
        blockSequence: Long,
        escrowId: String,
        messages: List<DeveloperChainMessage>,
        developerBlockSigner: DeveloperBlockSigner,
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

    private fun signDeveloperChainBlock(
        blockSequence: Long,
        escrowId: String,
        messages: List<DeveloperChainMessage>,
        developerBlockSigner: DeveloperBlockSigner,
    ): DeveloperChainBlock {
        return signDeveloperChainBlockWithState(
            blockSequence = blockSequence,
            escrowId = escrowId,
            messages = messages,
            developerBlockSigner = developerBlockSigner,
            baseState = DeterministicChainState(),
        ).block
    }

    private fun signDeveloperChainBlockWithExplicitStateHash(
        blockSequence: Long,
        escrowId: String,
        messages: List<DeveloperChainMessage>,
        developerBlockSigner: DeveloperBlockSigner,
        stateHash: String,
    ): DeveloperChainBlock {
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
        return DeveloperChainBlock(
            blockSequence = blockSequence,
            escrowId = escrowId,
            stateHash = stateHash,
            messages = messages,
            signature = signature,
        )
    }

    private fun extractInputTokens(openAiResponse: Any?): Long {
        val usage = (openAiResponse as? com.productscience.data.OpenAIResponse)?.usage
        return usage?.promptTokens?.toLong() ?: 0L
    }

    private fun extractOutputTokens(openAiResponse: Any?): Long {
        val usage = (openAiResponse as? com.productscience.data.OpenAIResponse)?.usage
        return usage?.completionTokens?.toLong() ?: 0L
    }

    private fun applyDeterministicState(
        baseState: DeterministicChainState,
        messages: List<DeveloperChainMessage>,
    ): DeterministicChainState {
        val nextState = baseState.copy(executorStats = baseState.executorStats.toMutableMap())
        messages.forEach { message ->
            when (message.type) {
                START_INFERENCE_MESSAGE_TYPE -> {
                    // StartInference does not mutate deterministic executor accounting.
                }
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
        val preimageHashHex = sha256Hex(preimage.toByteArray())
        return preimageHashHex.toByteArray(Charsets.UTF_8)
    }

    private fun verifySecp256k1Signature(signingPayload: ByteArray, pubKeyBytes: ByteArray, signatureBytes: ByteArray): Boolean {
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

            // Try Bouncy over all payload candidates.
            for (candidate in candidatePayloads) {
                if (signer.verifySignature(candidate, signatureParts.first, signatureParts.second)) {
                    return true
                }
            }

            // Try bitcoinj over 32-byte candidates.
            val bitcoinSig = ECKey.ECDSASignature(signatureParts.first, signatureParts.second)
            val bitcoinKey = ECKey.fromPublicOnly(pubKeyBytes)
            candidatePayloads
                .filter { it.size == 32 }
                .any { candidate -> bitcoinKey.verify(Sha256Hash.wrap(candidate), bitcoinSig) }
        } catch (_: Exception) {
            false
        }
    }

    private fun hexToBytes(hex: String): ByteArray {
        val clean = hex.trim()
        require(clean.length % 2 == 0) { "hex length must be even" }
        return ByteArray(clean.length / 2) { idx ->
            clean.substring(idx * 2, idx * 2 + 2).toInt(16).toByte()
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

    private fun tamperBase64Signature(signature: String): String {
        val raw = Base64.getDecoder().decode(signature).copyOf()
        raw[0] = (raw[0].toInt() xor 0x01).toByte()
        return Base64.getEncoder().encodeToString(raw)
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

    private fun selectResponsibleParticipantsDeterministic(
        participants: List<WeightedParticipant>,
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

    data class WeightedParticipant(
        val address: String,
        val weight: ULong,
    )

    data class V2StreamAcceptanceResult(
        val sequence: Long,
        val latestBlockSequence: Long,
    )

    data class ReservedStartInferencePlan(
        val sequence: Long,
        val recipientAddress: String,
        val developerChainDelta: DeveloperChainDelta,
    )

    data class StreamFanoutResult(
        val participantAddress: String,
        val latestBlockSequence: Long,
        val dataLines: List<String>,
        val executorProof: V2ExecutorProof?,
        val responsePayloadHash: String?,
    )

    data class V2StreamReadResult(
        val dataLines: List<String>,
        val executorProof: V2ExecutorProof?,
        val responsePayloadHash: String?,
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

    data class V2RawHttpResponse(
        val statusCode: Int,
        val body: String,
    )

    data class V2EscrowFixture(
        val genesis: LocalInferencePair,
        val allPairs: List<LocalInferencePair>,
        val developerAddress: String,
        val developerBlockSigner: DeveloperBlockSigner,
        val pairByAddress: Map<String, LocalInferencePair>,
        val weightedParticipants: List<WeightedParticipant>,
        val participantPubKeyByAddress: Map<String, String>,
    )

    data class EscrowContext(
        val escrowId: String,
        val epochId: Long,
    )

    data class DeveloperBlockSigner(
        val signingAccountAddress: String,
        val chainId: String,
        val signPayloadHex: (String) -> String,
    )

    data class DeveloperChainEnvelope(
        @SerializedName("openai_request")
        val openAiRequest: InferenceRequestPayload,
        val developerChainDelta: DeveloperChainDelta,
    )

    data class DeveloperChainDelta(
        val baseBlockSequence: Long,
        val blocks: List<DeveloperChainBlock>,
        val latestBlockSequence: Long,
    )

    data class SignedDeveloperChainBlock(
        val block: DeveloperChainBlock,
        val nextState: DeterministicChainState,
    )

    data class DeterministicChainState(
        val executorStats: MutableMap<String, DeterministicExecutorStats> = mutableMapOf(),
    )

    data class DeterministicExecutorStats(
        val processedInferences: Long = 0L,
        val inputTokenTotal: Long = 0L,
        val outputTokenTotal: Long = 0L,
        val missedInferences: Long = 0L,
    )

    data class V2MissedInferenceEvidencePayload(
        @SerializedName("relay_errors")
        val relayErrors: List<V2RelayErrorArtifact> = emptyList(),
    )

    data class DeveloperChainBlock(
        val blockSequence: Long,
        val escrowId: String,
        @SerializedName("state_hash")
        val stateHash: String,
        val messages: List<DeveloperChainMessage>,
        val signature: String,
    )

    data class DeveloperChainMessage(
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

    companion object {
        private const val EXPECTED_RESPONSIBLE_PARTICIPANTS = 3
        private const val START_INFERENCE_MESSAGE_TYPE = "StartInference"
        private const val FINISH_INFERENCE_MESSAGE_TYPE = "FinishInference"
        private const val MISSED_INFERENCE_MESSAGE_TYPE = "MissedInference"
        private const val DEV_BLOCK_MESSAGE_DOMAIN = "v2_dev_block_msg_v1"
        private const val DEV_BLOCK_SIGN_DOMAIN = "v2_dev_block_sig_v1"
        private const val DEV_STATE_HASH_DOMAIN = "v2_dev_state_hash_v1"
        private const val EXEC_FINISH_SIGN_DOMAIN = "v2_exec_finish_sig_v1"
        private const val V2_EXECUTOR_PROOF_EVENT = "v2_executor_proof"
    }
}
