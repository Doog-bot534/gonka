import com.productscience.LocalInferencePair
import com.productscience.V2InferenceResponse
import com.productscience.V2InferenceStreamConnection
import com.productscience.InferenceRequestPayload
import com.productscience.ClientFailure
import com.productscience.ClientSuccess
import com.productscience.CreateEscrowRequest
import com.productscience.EscrowSessionClient
import com.productscience.InferenceV2ClientConfig
import com.productscience.RequestOptions
import com.productscience.RetryCompletion
import com.productscience.RetryOptions
import com.productscience.RetryStream
import com.productscience.EnvelopeResignMode
import com.productscience.TestableEscrowSessionClient
import com.productscience.TestermintInferenceV2Client
import com.productscience.V2DeveloperBlockSigner
import com.productscience.V2WeightedParticipant
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
        val pairByAddress = fixture.pairByAddress
        val weightedParticipants = fixture.weightedParticipants
        val developerBlockSigner = fixture.developerBlockSigner
        val client = createV2DeveloperClient(fixture)

        logSection("Developer creates escrow")
        val session = client.createEscrow(CreateEscrowRequest(modelId = defaultModel))
        val escrowContext = session.snapshot().context
        val escrowId = escrowContext.escrowId
        val epochId = escrowContext.epochId
        Logger.info(
            "V2_ESCROW_TEST created escrow_id={} developer={} participants={}",
            escrowId,
            fixture.developerAddress,
            pairByAddress.keys.sorted(),
        )

        logSection("Sending 10 v2 requests with deterministic executor choice")
        repeat(10) { requestIndex ->
            val openAiRequest = buildOpenAiRequest(sequence = requestIndex + 1L, stream = false)
            val outcome = session.complete(openAiRequest)
            assertThat(outcome).isInstanceOf(ClientSuccess::class.java)
            val result = (outcome as ClientSuccess).result

            Logger.info(
                "V2_ESCROW_TEST sequence={} chosen_executor={} responsible_participants={} response_id={}",
                result.sequence,
                result.recipientAddress,
                result.responsibleParticipants,
                result.value.id,
            )

            assertThat(result.sequence).isEqualTo(requestIndex + 1L)
            assertThat(result.recipientAddress).isEqualTo(result.responsibleParticipants.first())
            assertThat(result.value.id).isNotBlank()
            assertThat(result.value.model).isEqualTo(defaultModel)
            assertThat(result.value.choices).isNotEmpty()
            assertThat(result.latestBlockSequence).isEqualTo(result.sequence)
        }

        logSection("Sending stale base_block_sequence request and expecting rejection")
        val invalidSequence = 11L
        val invalidResponsibleParticipants = selectResponsibleParticipantsDeterministic(
            participants = weightedParticipants,
            selectionCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
            escrowId = escrowId,
            sequence = invalidSequence,
        )
        val acknowledgedByRecipient = session.snapshot().acknowledgedByRecipient
        val invalidTargetAddress = invalidResponsibleParticipants
            .maxByOrNull { acknowledgedByRecipient[it] ?: 0L }
            ?: invalidResponsibleParticipants.first()
        val invalidTargetPair = requireNotNull(pairByAddress[invalidTargetAddress]) {
            "Missing pair for invalid continuity request"
        }
        val receiverLatestBlockSequence = acknowledgedByRecipient[invalidTargetAddress] ?: 0L
        assertThat(receiverLatestBlockSequence).isGreaterThan(0L)
        val staleBaseBlockSequence = receiverLatestBlockSequence - 1
        val invalidOpenAiRequest = buildOpenAiRequest(sequence = invalidSequence, stream = false)
        val invalidHandle = (session as TestableEscrowSessionClient).reserveHandleForTesting(invalidOpenAiRequest)
        val invalidOutcome = session.retryWithModifiedEnvelopeForTesting(
            handle = invalidHandle,
            options = RetryOptions(preferredInitialRecipient = invalidTargetAddress),
            resignMode = EnvelopeResignMode.RECOMPUTE_STATE_AND_SIGN,
        ) { envelope ->
            val developerChainDelta = envelope.getAsJsonObject("developerChainDelta")
                ?: envelope.getAsJsonObject("developer_chain_delta")
                ?: error("Missing developerChainDelta in envelope")
            val mutatedBlock = com.google.gson.JsonObject().apply {
                addProperty("block_sequence", staleBaseBlockSequence + 1)
                addProperty("escrow_id", escrowId)
                addProperty("state_hash", "")
                add("messages", com.google.gson.JsonArray().apply {
                    add(com.google.gson.JsonObject().apply {
                        addProperty("type", START_INFERENCE_MESSAGE_TYPE)
                        addProperty("request_id", buildV2RequestId(escrowId, invalidSequence))
                        addProperty("model_id", defaultModel)
                        addProperty("request_payload_hash", computeV2RequestPayloadHash(invalidOpenAiRequest))
                        addProperty("timestamp", System.currentTimeMillis() / 1000)
                    })
                })
                addProperty("signature", "")
            }
            developerChainDelta.addProperty("base_block_sequence", staleBaseBlockSequence)
            developerChainDelta.add("blocks", com.google.gson.JsonArray().apply { add(mutatedBlock) })
            developerChainDelta.addProperty("latest_block_sequence", staleBaseBlockSequence + 1)
        }
        assertThat(invalidOutcome).isInstanceOf(ClientFailure::class.java)
        val invalidFailure = invalidOutcome as ClientFailure
        assertThat(invalidFailure.error.message).contains("409")
    }

    @Test
    fun `developer client creates escrow and completes non-streaming request`() {
        logSection("V2 developer client happy-path setup")
        val fixture = setupV2EscrowFixture()
        val client = createV2DeveloperClient(fixture)
        val session = client.createEscrow(CreateEscrowRequest(modelId = defaultModel))

        val outcome = session.complete(
            request = buildOpenAiRequest(sequence = 1L, stream = false),
        )

        assertThat(outcome).isInstanceOf(ClientSuccess::class.java)
        val success = outcome as ClientSuccess
        assertThat(success.result.sequence).isEqualTo(1L)
        assertThat(success.result.requestId).isEqualTo("${session.snapshot().context.escrowId}:1")
        assertThat(success.result.value.id).isNotBlank()
        assertThat(success.result.value.model).isEqualTo(defaultModel)
        assertThat(success.result.latestBlockSequence).isEqualTo(1L)
        assertThat(session.snapshot().latestReservedSequence).isEqualTo(1L)
        assertThat(session.snapshot().chainTipSequence).isEqualTo(1L)
    }

    @Test
    fun `developer client returns retry handle and retries same logical request through all responsible participants`() {
        logSection("V2 developer client retry handle setup")
        val fixture = setupV2EscrowFixture()
        val client = createV2DeveloperClient(fixture)
        val session = client.createEscrow(CreateEscrowRequest(modelId = defaultModel))
        val escrowId = session.snapshot().context.escrowId
        val responsibleParticipants = selectResponsibleParticipantsDeterministic(
            participants = fixture.weightedParticipants,
            selectionCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
            escrowId = escrowId,
            sequence = 1L,
        )
        val failingRecipient = responsibleParticipants[1]
        val failingPair = requireNotNull(fixture.pairByAddress[failingRecipient]) {
            "Missing pair for failing participant=$failingRecipient"
        }

        setApiContainerRunning(failingPair, running = false)
        try {
            val failureOutcome = session.complete(
                request = buildOpenAiRequest(sequence = 1L, stream = false),
                options = RequestOptions(
                    preferredInitialRecipient = failingRecipient,
                    allowFanoutOnTimeout = false,
                ),
            )

            assertThat(failureOutcome).isInstanceOf(ClientFailure::class.java)
            val failure = failureOutcome as ClientFailure
            assertThat(failure.retryHandle).isNotNull()
            assertThat(failure.retryHandle?.sequence).isEqualTo(1L)
            assertThat(failure.attempts).hasSize(1)
            assertThat(failure.attempts.single().recipientAddress).isEqualTo(failingRecipient)

            val retryOutcome = session.retry(
                handle = requireNotNull(failure.retryHandle),
                options = RetryOptions(resendToAllResponsible = true),
            )

            assertThat(retryOutcome).isInstanceOf(ClientSuccess::class.java)
            val retrySuccess = retryOutcome as ClientSuccess
            val retryResult = retrySuccess.result
            assertThat(retryResult).isInstanceOf(RetryCompletion::class.java)
            val completion = (retryResult as RetryCompletion).result
            assertThat(completion.sequence).isEqualTo(1L)
            assertThat(completion.requestId).isEqualTo("$escrowId:1")
            assertThat(completion.value.id).isNotBlank()
            assertThat(completion.attempts.map { it.recipientAddress }).containsAll(responsibleParticipants)
        } finally {
            setApiContainerRunning(failingPair, running = true)
        }
    }

    @Test
    fun `developer sends overlapping v2 streaming requests in parallel`() {
        logSection("V2 parallel streaming overlap test setup")
        val fixture = setupV2EscrowFixture()
        val allPairs = fixture.allPairs
        val client = createV2DeveloperClient(fixture)

        allPairs.forEach { pair ->
            pair.mock?.setInferenceResponse(
                openAIResponse = defaultInferenceResponseObject,
                delay = Duration.ofSeconds(5),
                streamDelay = Duration.ofMillis(50),
                model = defaultModel,
            )
        }

        val session = client.createEscrow(CreateEscrowRequest(modelId = defaultModel))
        val requestCount = 12

        val executor = Executors.newFixedThreadPool(requestCount)
        val startGate = CountDownLatch(1)
        try {
            val futures = (1..requestCount).map {
                executor.submit<V2StreamAcceptanceResult> {
                    startGate.await()
                    val outcome = session.stream(inferenceRequestObject.copy(stream = true))
                    assertThat(outcome).isInstanceOf(ClientSuccess::class.java)
                    val result = (outcome as ClientSuccess).result
                    try {
                        V2StreamAcceptanceResult(
                            sequence = result.sequence,
                            latestBlockSequence = result.latestBlockSequence,
                        )
                    } finally {
                        result.stream.close()
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
        val client = createV2DeveloperClient(fixture)
        val session = client.createEscrow(CreateEscrowRequest(modelId = defaultModel))
        val outcome = session.complete(buildOpenAiRequest(sequence = 1L, stream = false))
        assertThat(outcome).isInstanceOf(ClientSuccess::class.java)
        val result = (outcome as ClientSuccess).result
        val testSession = session as TestableEscrowSessionClient
        val handle = testSession.retryHandleForSequence(1L)
        val tamperedSignature = tamperBase64Signature(requireNotNull(result.executorSignature))
        assertThatThrownBy {
            testSession.recordFinishForTesting(
                handle = handle,
                openAiResponse = result.value,
                responsePayloadHash = result.responsePayloadHash
                    ?: computeV2ResponsePayloadHash(result.value),
                executorAddress = result.executorAddress,
                executorSignerAddress = result.executorSignerAddress,
                executorSignerPubKey = result.executorSignerPubKey,
                executorSignature = tamperedSignature,
            )
        }.isInstanceOf(IllegalStateException::class.java)
    }

    @Test
    fun `developer retries same logical request through all responsible participants`() {
        logSection("V2 relay retry/fanout test setup")
        val fixture = setupV2EscrowFixture()
        val allPairs = fixture.allPairs
        val client = createV2DeveloperClient(fixture)
        val session = client.createEscrow(CreateEscrowRequest(modelId = defaultModel))
        val escrowId = session.snapshot().context.escrowId

        val sequence = 1L
        val openAiRequest = buildOpenAiRequest(sequence = sequence, stream = false)
        val requestId = buildV2RequestId(escrowId, sequence)

        val responsibleParticipants = selectResponsibleParticipantsDeterministic(
            participants = fixture.weightedParticipants,
            selectionCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
            escrowId = escrowId,
            sequence = sequence,
        )
        assertThat(responsibleParticipants).hasSize(3)
        val intendedExecutor = responsibleParticipants.first()
        val intendedPair = requireNotNull(fixture.pairByAddress[intendedExecutor]) {
            "Missing pair for intended executor=$intendedExecutor"
        }
        intendedPair.mock?.setInferenceResponse(
            openAIResponse = defaultInferenceResponseObject,
            delay = Duration.ofSeconds(3),
            streamDelay = Duration.ofMillis(50),
            model = defaultModel,
        )
        val outcome = session.complete(
            request = openAiRequest,
            options = RequestOptions(sendToAllResponsible = true),
        )
        assertThat(outcome).isInstanceOf(ClientSuccess::class.java)
        val completion = (outcome as ClientSuccess).result
        val successResponseIds = completion.attempts
            .mapNotNull { attempt -> attempt.responseId }
        assertThat(successResponseIds).hasSize(responsibleParticipants.size)
        assertThat(successResponseIds.distinct()).hasSize(1)
        completion.attempts.forEach { attempt ->
            assertThat(attempt.latestBlockSequence).isEqualTo(sequence)
        }
        assertThat(completion.sequence).isEqualTo(sequence)
        assertThat(completion.requestId).isEqualTo(requestId)
        assertThat(completion.value.id).isNotBlank()

        // Replay after acceptance should remain deterministic for the same logical request identity.
        val replayHandle = (session as TestableEscrowSessionClient).retryHandleForSequence(sequence)
        val replayOutcome = session.retry(
            handle = replayHandle,
            options = RetryOptions(preferredInitialRecipient = responsibleParticipants[1]),
        )
        assertThat(replayOutcome).isInstanceOf(ClientSuccess::class.java)
        val replayResult = (replayOutcome as ClientSuccess).result
        assertThat(replayResult).isInstanceOf(RetryCompletion::class.java)
        val replayCompletion = (replayResult as RetryCompletion).result
        assertThat(replayCompletion.value.id).isEqualTo(completion.value.id)
        assertThat(replayCompletion.latestBlockSequence).isEqualTo(sequence)
        Logger.info(
            "V2_STEP11 fanout completed request_id={} intended_executor={} recipients={}",
            requestId,
            intendedExecutor,
            responsibleParticipants,
        )
    }

    @Test
    fun `developer receives streamed replay from all responsible participants for same request id`() {
        logSection("V2 streaming fanout replay test setup")
        val fixture = setupV2EscrowFixture()
        val allPairs = fixture.allPairs
        val client = createV2DeveloperClient(fixture)
        allPairs.forEach { pair ->
            pair.mock?.setInferenceResponse(
                openAIResponse = defaultInferenceResponseObject,
                delay = Duration.ofSeconds(3),
                streamDelay = Duration.ofMillis(50),
                model = defaultModel,
            )
        }

        val session = client.createEscrow(CreateEscrowRequest(modelId = defaultModel))
        val testSession = session as TestableEscrowSessionClient
        val escrowId = session.snapshot().context.escrowId
        val sequence = 1L
        val leaderOutcome = session.stream(buildOpenAiRequest(sequence = sequence, stream = true))
        assertThat(leaderOutcome).isInstanceOf(ClientSuccess::class.java)
        val leaderConnection = (leaderOutcome as ClientSuccess).result
        assertThat(leaderConnection.sequence).isEqualTo(sequence)
        val responsibleParticipants = leaderConnection.responsibleParticipants
        val requestBlockSignature = cosmosJson.fromJson(
            testSession.blockJsonForSequence(sequence),
            com.google.gson.JsonObject::class.java,
        ).get("signature").asString

        val pool = Executors.newFixedThreadPool(responsibleParticipants.size - 1)
        try {
            val futures = responsibleParticipants.drop(1).map { participantAddress ->
                pool.submit<StreamFanoutResult> {
                    val retryOutcome = session.retry(
                        handle = leaderConnection.retryHandle,
                        options = RetryOptions(preferredInitialRecipient = participantAddress),
                    )
                    assertThat(retryOutcome).isInstanceOf(ClientSuccess::class.java)
                    val retryResult = (retryOutcome as ClientSuccess).result
                    assertThat(retryResult).isInstanceOf(RetryStream::class.java)
                    val connection = (retryResult as RetryStream).result
                    try {
                        val streamResult = readV2StreamDataAndProof(connection.stream)
                        StreamFanoutResult(
                            participantAddress = participantAddress,
                            latestBlockSequence = connection.latestBlockSequence,
                            dataLines = streamResult.dataLines,
                            executorProof = streamResult.executorProof,
                            responsePayloadHash = streamResult.responsePayloadHash,
                        )
                    } finally {
                        connection.stream.close()
                    }
                }
            }
            val leaderResult = try {
                val streamResult = readV2StreamDataAndProof(leaderConnection.stream)
                StreamFanoutResult(
                    participantAddress = leaderConnection.recipientAddress,
                    latestBlockSequence = leaderConnection.latestBlockSequence,
                    dataLines = streamResult.dataLines,
                    executorProof = streamResult.executorProof,
                    responsePayloadHash = streamResult.responsePayloadHash,
                )
            } finally {
                leaderConnection.stream.close()
            }
            val results = listOf(leaderResult) + futures.map { it.get(60, TimeUnit.SECONDS) }
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
            val finishRequestId = buildV2RequestId(escrowId, sequence)
            val nextOutcome = session.complete(buildOpenAiRequest(sequence = sequence + 1, stream = false))
            assertThat(nextOutcome).isInstanceOf(ClientSuccess::class.java)
            val block2 = cosmosJson.fromJson(
                testSession.blockJsonForSequence(sequence + 1L),
                com.google.gson.JsonObject::class.java,
            )
            val messages = block2.getAsJsonArray("messages")
            val persistedFinish = messages
                .map { it.asJsonObject }
                .firstOrNull { message ->
                    val requestIdField = message.get("requestId") ?: message.get("request_id")
                    message.get("type").asString == FINISH_INFERENCE_MESSAGE_TYPE &&
                        requestIdField?.asString == finishRequestId
                }
            assertThat(persistedFinish).isNotNull()
            assertThat((persistedFinish?.get("responsePayloadHash") ?: persistedFinish?.get("response_payload_hash"))?.asString)
                .isEqualTo(firstHash)
            assertThat((persistedFinish?.get("executorAddress") ?: persistedFinish?.get("executor_address"))?.asString)
                .isEqualTo(firstProof.executorAddress)
            assertThat((persistedFinish?.get("executorSignerAddress") ?: persistedFinish?.get("executor_signer_address"))?.asString)
                .isEqualTo(firstProof.executorSignerAddress)
            assertThat((persistedFinish?.get("executorSignerPubKey") ?: persistedFinish?.get("executor_signer_pubkey"))?.asString)
                .isEqualTo(firstProof.executorSignerPubKey)
            assertThat((persistedFinish?.get("executorSignature") ?: persistedFinish?.get("executor_signature"))?.asString)
                .isEqualTo(firstProof.executorSignature)
            assertThat((persistedFinish?.get("inputTokenCount") ?: persistedFinish?.get("input_token_count"))?.asLong)
                .isEqualTo(defaultInferenceResponseObject.usage.promptTokens.toLong())
            assertThat((persistedFinish?.get("outputTokenCount") ?: persistedFinish?.get("output_token_count"))?.asLong)
                .isEqualTo(defaultInferenceResponseObject.usage.completionTokens.toLong())

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
        val client = createV2DeveloperClient(fixture)
        val session = client.createEscrow(CreateEscrowRequest(modelId = defaultModel))
        val escrowId = session.snapshot().context.escrowId

        val sequence = 1L
        val responsibleParticipants = selectResponsibleParticipantsDeterministic(
            participants = fixture.weightedParticipants,
            selectionCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
            escrowId = escrowId,
            sequence = sequence,
        )
        assertThat(responsibleParticipants).hasSize(3)
        val nonIntendedParticipant = responsibleParticipants[1]
        val outcome = session.stream(
            request = buildOpenAiRequest(sequence = sequence, stream = true),
            options = RequestOptions(preferredInitialRecipient = nonIntendedParticipant),
        )
        assertThat(outcome).isInstanceOf(ClientSuccess::class.java)
        val streamConnection = (outcome as ClientSuccess).result
        try {
            assertThat(streamConnection.latestBlockSequence).isEqualTo(sequence)
            val firstLine = streamConnection.stream.readLine()
            assertThat(firstLine).isNotNull
            assertThat(firstLine).contains("data:")
        } finally {
            streamConnection.stream.close()
        }
    }

    @Test
    fun `leader disconnect mid-stream and follower still receives replay`() {
        logSection("V2 step13 streaming disconnect resilience test setup")
        val fixture = setupV2EscrowFixture()
        val allPairs = fixture.allPairs
        val pairByAddress = fixture.pairByAddress
        val weightedParticipants = fixture.weightedParticipants
        val client = createV2DeveloperClient(fixture)
        allPairs.forEach { pair ->
            pair.mock?.setInferenceResponse(
                openAIResponse = defaultInferenceResponseObject,
                delay = Duration.ofSeconds(3),
                streamDelay = Duration.ofMillis(40),
                model = defaultModel,
            )
        }

        val session = client.createEscrow(CreateEscrowRequest(modelId = defaultModel))
        val escrowId = session.snapshot().context.escrowId

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

        val leaderOutcome = session.stream(
            request = buildOpenAiRequest(sequence = sequence, stream = true),
            options = RequestOptions(preferredInitialRecipient = intendedExecutor),
        )
        assertThat(leaderOutcome).isInstanceOf(ClientSuccess::class.java)
        val leaderConnection = (leaderOutcome as ClientSuccess).result
        assertThat(leaderConnection.latestBlockSequence).isEqualTo(sequence)
        leaderConnection.stream.readLine() // consume one line to ensure stream started
        leaderConnection.stream.close()

        // Follower should still receive replay/history for same request identity.
        val followerOutcome = session.retry(
            handle = leaderConnection.retryHandle,
            options = RetryOptions(preferredInitialRecipient = responsibleParticipants[1]),
        )
        assertThat(followerOutcome).isInstanceOf(ClientSuccess::class.java)
        val followerResult = (followerOutcome as ClientSuccess).result
        // Retry over a stream request returns RetryStream.
        assertThat(followerResult).isInstanceOf(com.productscience.RetryStream::class.java)
        val followerConnection = (followerResult as com.productscience.RetryStream).result
        try {
            val replayData = readSSEDataLines(followerConnection.stream)
            assertThat(replayData).isNotEmpty()
            assertThat(followerConnection.latestBlockSequence).isEqualTo(sequence)
        } finally {
            followerConnection.stream.close()
        }
    }

    @Test
    fun `leader timeout on non-streaming and follower still gets completed replay`() {
        logSection("V2 step13 non-streaming disconnect resilience test setup")
        val fixture = setupV2EscrowFixture()
        val pairByAddress = fixture.pairByAddress
        val weightedParticipants = fixture.weightedParticipants
        val client = createV2DeveloperClient(fixture)
        val session = client.createEscrow(CreateEscrowRequest(modelId = defaultModel))
        val escrowId = session.snapshot().context.escrowId

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

        val failureOutcome = session.complete(
            request = buildOpenAiRequest(sequence = sequence, stream = false),
            options = RequestOptions(
                preferredInitialRecipient = intendedExecutor,
                leaderTimeout = Duration.ofMillis(150),
                allowFanoutOnTimeout = false,
            ),
        )
        assertThat(failureOutcome).isInstanceOf(ClientFailure::class.java)
        val failure = failureOutcome as ClientFailure
        assertThat(failure.retryHandle).isNotNull()
        assertThat(failure.attempts).hasSize(1)
        assertThat(failure.attempts.single().recipientAddress).isEqualTo(intendedExecutor)

        val followerOutcome = session.retry(
            handle = requireNotNull(failure.retryHandle),
            options = RetryOptions(preferredInitialRecipient = responsibleParticipants[1]),
        )
        assertThat(followerOutcome).isInstanceOf(ClientSuccess::class.java)
        val retryResult = (followerOutcome as ClientSuccess).result
        assertThat(retryResult).isInstanceOf(RetryCompletion::class.java)
        val followerResponse = (retryResult as RetryCompletion).result
        assertThat(followerResponse.latestBlockSequence).isEqualTo(sequence)
        assertThat(followerResponse.value.id).isNotBlank()
    }

    @Test
    fun `relay participant ingests chain and rejects overlap mismatch before forwarding`() {
        logSection("V2 relay-side chain ingestion test setup")
        val fixture = setupV2EscrowFixture()
        val pairByAddress = fixture.pairByAddress
        val weightedParticipants = fixture.weightedParticipants
        val client = createV2DeveloperClient(fixture)
        val session = client.createEscrow(CreateEscrowRequest(modelId = defaultModel))
        val testSession = session as TestableEscrowSessionClient
        val escrowId = session.snapshot().context.escrowId

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

        val handle1 = testSession.reserveHandleForTesting(buildOpenAiRequest(sequence = sequence1, stream = false))
        val request1Outcome = testSession.retryForTesting(
            handle = handle1,
            options = RetryOptions(preferredInitialRecipient = relayParticipant),
            recordFinishOnSuccess = false,
        )
        assertThat(request1Outcome).isInstanceOf(ClientSuccess::class.java)
        val response1 = ((request1Outcome as ClientSuccess).result as RetryCompletion).result
        assertThat(response1.latestBlockSequence).isEqualTo(sequence1)
        // Give the relay node a block to durably ingest the forwarded chain update before the next request.
        fixture.genesis.node.waitForNextBlock(1)

        val sequence2 = 2L
        val handle2 = testSession.reserveHandleForTesting(buildOpenAiRequest(sequence = sequence2, stream = false))
        val request2Outcome = testSession.retryForTesting(
            handle = handle2,
            options = RetryOptions(preferredInitialRecipient = relayParticipant),
            recordFinishOnSuccess = false,
        )
        assertThat(request2Outcome).isInstanceOf(ClientSuccess::class.java)
        val response2 = ((request2Outcome as ClientSuccess).result as RetryCompletion).result
        assertThat(response2.latestBlockSequence).isEqualTo(sequence2)

        val sequence3 = 3L
        val request3 = buildOpenAiRequest(sequence = sequence3, stream = false)
        val request3Handle = testSession.reserveHandleForTesting(request3)
        val request3Outcome = session.retryWithModifiedEnvelopeForTesting(
            handle = request3Handle,
            options = RetryOptions(preferredInitialRecipient = relayParticipant),
            resignMode = EnvelopeResignMode.RECOMPUTE_STATE_AND_SIGN,
        ) { envelope ->
            val developerChainDelta = envelope.getAsJsonObject("developerChainDelta")
                ?: envelope.getAsJsonObject("developer_chain_delta")
                ?: error("Missing developerChainDelta in envelope")
            val block2 = cosmosJson.fromJson(
                testSession.blockJsonForSequence(2L),
                com.google.gson.JsonObject::class.java,
            )
            val block2Messages = block2.getAsJsonArray("messages")
            val block2Start = block2Messages.first().asJsonObject
            block2Start.addProperty("request_payload_hash", "deadbeef")
            block2Start.addProperty("timestamp", (System.currentTimeMillis() / 1000) + 77)
            val block3 = cosmosJson.fromJson(
                testSession.blockJsonForSequence(3L),
                com.google.gson.JsonObject::class.java,
            )
            developerChainDelta.addProperty("base_block_sequence", 1)
            developerChainDelta.add("blocks", com.google.gson.JsonArray().apply {
                add(block2)
                add(block3)
            })
            developerChainDelta.addProperty("latest_block_sequence", 3)
        }
        assertThat(request3Outcome).isInstanceOf(ClientFailure::class.java)
        val request3Failure = request3Outcome as ClientFailure
        assertThat(request3Failure.error.message).contains("409")
    }

    @Test
    fun `relay failures return signed artifacts from distinct relays`() {
        logSection("V2 Step16 relay signed-error quorum test setup")
        val fixture = setupV2EscrowFixture()
        val pairByAddress = fixture.pairByAddress
        val weightedParticipants = fixture.weightedParticipants
        val client = createV2DeveloperClient(fixture)
        val session = client.createEscrow(CreateEscrowRequest(modelId = defaultModel))
        val testSession = session as TestableEscrowSessionClient
        val escrowId = session.snapshot().context.escrowId

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
        val openAiRequest1 = buildOpenAiRequest(sequence = sequence1, stream = false)
        val requestId1 = buildV2RequestId(escrowId, sequence1)
        val handle = testSession.reserveHandleForTesting(openAiRequest1)

        setApiContainerRunning(intendedPair, running = false)
        try {
            val failureOutcome = session.retry(
                handle = handle,
                options = RetryOptions(resendToAllResponsible = true),
            )
            assertThat(failureOutcome).isInstanceOf(ClientFailure::class.java)
            val failure = failureOutcome as ClientFailure
            val artifacts = failure.relayArtifacts
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
            assertThat(com.productscience.verifyV2RelayErrorArtifactSignature(artifactA)).isTrue()
            assertThat(com.productscience.verifyV2RelayErrorArtifactSignature(artifactB)).isTrue()
            assertThat(setOf(artifactA.relayAddress, artifactB.relayAddress)).hasSize(2)
            assertThat(artifactA.failureCode).isNotBlank()
            assertThat(artifactB.failureCode).isNotBlank()
            assertThat(failure.missedInferenceQueued).isTrue()

            setApiContainerRunning(intendedPair, running = true)
            intendedPair.node.waitForNextBlock(1)
            val nextOutcome = session.complete(buildOpenAiRequest(sequence = 2L, stream = false))
            assertThat(nextOutcome).isInstanceOf(ClientSuccess::class.java)
            val block2 = cosmosJson.fromJson(
                testSession.blockJsonForSequence(2L),
                com.google.gson.JsonObject::class.java,
            )
            val messages = block2.getAsJsonArray("messages")
            val missed = messages
                .map { it.asJsonObject }
                .firstOrNull { message ->
                    val requestIdField = message.get("requestId") ?: message.get("request_id")
                    message.get("type").asString == MISSED_INFERENCE_MESSAGE_TYPE &&
                        requestIdField?.asString == requestId1
                }
            assertThat(missed).isNotNull()
            val evidenceJson = (missed?.get("missedInferenceEvidence") ?: missed?.get("missed_inference_evidence"))?.asString
            assertThat(evidenceJson).isNotBlank()
            val evidence = cosmosJson.fromJson(
                evidenceJson,
                com.google.gson.JsonObject::class.java,
            )
            val relayErrors = evidence.getAsJsonArray("relay_errors")
            assertThat(relayErrors.map { it.asJsonObject.get("relay_address").asString }.toSet())
                .containsExactlyInAnyOrder(relayAddressA, relayAddressB)
        } finally {
            setApiContainerRunning(intendedPair, running = true)
        }
    }

    @Test
    fun `state hash converges across responsible participants and mismatched signed state hash is rejected`() {
        logSection("V2 Step17 deterministic state hash convergence test setup")
        val fixture = setupV2EscrowFixture()
        val weightedParticipants = fixture.weightedParticipants
        val client = createV2DeveloperClient(fixture)
        val session = client.createEscrow(CreateEscrowRequest(modelId = defaultModel))
        val testSession = session as TestableEscrowSessionClient
        val escrowId = session.snapshot().context.escrowId

        val sequence1 = 1L
        val outcome1 = session.complete(buildOpenAiRequest(sequence = sequence1, stream = false))
        assertThat(outcome1).isInstanceOf(ClientSuccess::class.java)
        val response1 = (outcome1 as ClientSuccess).result
        assertThat(response1.latestBlockSequence).isEqualTo(sequence1)

        val sequence2 = 2L
        val responsibleForSeq2 = selectResponsibleParticipantsDeterministic(
            participants = weightedParticipants,
            selectionCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
            escrowId = escrowId,
            sequence = sequence2,
        )
        val outcome2 = session.complete(
            request = buildOpenAiRequest(sequence = sequence2, stream = false),
            options = RequestOptions(sendToAllResponsible = true),
        )
        assertThat(outcome2).isInstanceOf(ClientSuccess::class.java)
        val response2 = (outcome2 as ClientSuccess).result
        assertThat(response2.attempts.mapNotNull { it.responseId }).hasSize(responsibleForSeq2.size)
        response2.attempts.forEach { attempt ->
            assertThat(attempt.latestBlockSequence).isEqualTo(sequence2)
        }

        val sequence3 = 3L
        val intendedForSeq3 = selectResponsibleParticipantsDeterministic(
            participants = weightedParticipants,
            selectionCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
            escrowId = escrowId,
            sequence = sequence3,
        ).first()
        val handle3 = testSession.reserveHandleForTesting(buildOpenAiRequest(sequence = sequence3, stream = false))
        val outcome3 = session.retryWithModifiedEnvelopeForTesting(
            handle = handle3,
            options = RetryOptions(preferredInitialRecipient = intendedForSeq3),
            resignMode = EnvelopeResignMode.SIGN_USING_EXISTING_STATE_HASH,
        ) { envelope ->
            val developerChainDelta = envelope.getAsJsonObject("developerChainDelta")
                ?: envelope.getAsJsonObject("developer_chain_delta")
                ?: error("Missing developerChainDelta in envelope")
            val block3 = cosmosJson.fromJson(
                testSession.blockJsonForSequence(3L),
                com.google.gson.JsonObject::class.java,
            )
            block3.addProperty("state_hash", "deadbeef")
            developerChainDelta.addProperty("base_block_sequence", 2)
            developerChainDelta.add("blocks", com.google.gson.JsonArray().apply { add(block3) })
            developerChainDelta.addProperty("latest_block_sequence", 3)
        }
        assertThat(outcome3).isInstanceOf(ClientFailure::class.java)
        val failure3 = outcome3 as ClientFailure
        assertThat(failure3.error.message).contains("409")
    }

    @Test
    fun `conflicting overlap invalidates escrow and all participants reject subsequent requests`() {
        logSection("V2 Step15 escrow invalidation E2E setup")
        val fixture = setupV2EscrowFixture()
        val allPairs = fixture.allPairs
        val weightedParticipants = fixture.weightedParticipants
        val client = createV2DeveloperClient(fixture)
        val session = client.createEscrow(CreateEscrowRequest(modelId = defaultModel))
        val testSession = session as TestableEscrowSessionClient
        val escrowId = session.snapshot().context.escrowId

        val intendedForSeq3 = selectResponsibleParticipantsDeterministic(
            participants = weightedParticipants,
            selectionCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
            escrowId = escrowId,
            sequence = 3L,
        ).first()
        val outcome1 = session.complete(buildOpenAiRequest(sequence = 1L, stream = false))
        assertThat(outcome1).isInstanceOf(ClientSuccess::class.java)
        val response1 = (outcome1 as ClientSuccess).result
        assertThat(response1.latestBlockSequence).isEqualTo(1L)

        val outcome2 = session.complete(buildOpenAiRequest(sequence = 2L, stream = false))
        assertThat(outcome2).isInstanceOf(ClientSuccess::class.java)
        val response2 = (outcome2 as ClientSuccess).result
        assertThat(response2.latestBlockSequence).isEqualTo(2L)

        val handle3 = testSession.reserveHandleForTesting(buildOpenAiRequest(sequence = 3L, stream = false))
        val outcome3 = session.retryWithModifiedEnvelopeForTesting(
            handle = handle3,
            options = RetryOptions(preferredInitialRecipient = intendedForSeq3),
            resignMode = EnvelopeResignMode.RECOMPUTE_STATE_AND_SIGN,
        ) { envelope ->
            val developerChainDelta = envelope.getAsJsonObject("developerChainDelta")
                ?: envelope.getAsJsonObject("developer_chain_delta")
                ?: error("Missing developerChainDelta in envelope")
            val block2 = cosmosJson.fromJson(
                testSession.blockJsonForSequence(2L),
                com.google.gson.JsonObject::class.java,
            )
            val block2Messages = block2.getAsJsonArray("messages")
            val block2Start = block2Messages.first().asJsonObject
            block2Start.addProperty("request_payload_hash", "deadbeef")
            block2Start.addProperty("timestamp", (System.currentTimeMillis() / 1000) + 77)
            val block3 = cosmosJson.fromJson(
                testSession.blockJsonForSequence(3L),
                com.google.gson.JsonObject::class.java,
            )
            developerChainDelta.addProperty("base_block_sequence", 1)
            developerChainDelta.add("blocks", com.google.gson.JsonArray().apply {
                add(block2)
                add(block3)
            })
            developerChainDelta.addProperty("latest_block_sequence", 3)
        }
        assertThat(outcome3).isInstanceOf(ClientFailure::class.java)
        val failure3 = outcome3 as ClientFailure
        assertThat(failure3.error.message).contains("409")

        // Wait until all API nodes ingest the on-chain escrow_invalidated event.
        allPairs.forEach { it.node.waitForNextBlock(2) }
        // Full-class runs can drift into non-inference phases here; wait for the next inference window
        // so subsequent rejections reflect escrow invalidation rather than temporary node unavailability.
        fixture.genesis.waitForNextInferenceWindow()
        fixture.allPairs.forEach { it.waitForMlNodesToLoad() }

        val responsibleForSeq4 = selectResponsibleParticipantsDeterministic(
            participants = weightedParticipants,
            selectionCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
            escrowId = escrowId,
            sequence = 4L,
        )
        val handle4 = testSession.reserveHandleForTesting(buildOpenAiRequest(sequence = 4L, stream = false))
        responsibleForSeq4.forEach { participantAddress ->
            val outcome4 = session.retry(
                handle = handle4,
                options = RetryOptions(preferredInitialRecipient = participantAddress),
            )
            assertThat(outcome4).isInstanceOf(ClientFailure::class.java)
            val failure4 = outcome4 as ClientFailure
            assertThat(failure4.error.message).contains("409")
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

    private fun createV2DeveloperClient(fixture: V2EscrowFixture): TestermintInferenceV2Client {
        return TestermintInferenceV2Client(
            InferenceV2ClientConfig(
                genesis = fixture.genesis,
                allPairs = fixture.allPairs,
                developerAddress = fixture.developerAddress,
                developerBlockSigner = V2DeveloperBlockSigner(
                    signingAccountAddress = fixture.developerBlockSigner.signingAccountAddress,
                    chainId = fixture.developerBlockSigner.chainId,
                    signPayloadHex = fixture.developerBlockSigner.signPayloadHex,
                ),
                pairByAddress = fixture.pairByAddress,
                weightedParticipants = fixture.weightedParticipants.map { participant ->
                    V2WeightedParticipant(
                        address = participant.address,
                        weight = participant.weight,
                    )
                },
                responsibleParticipantCount = EXPECTED_RESPONSIBLE_PARTICIPANTS,
            )
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

    private fun readSSEDataLines(streamConnection: com.productscience.LineReadableStream): List<String> {
        return readV2StreamDataAndProof(streamConnection).dataLines
    }

    private fun readV2StreamDataAndProof(streamConnection: com.productscience.LineReadableStream): V2StreamReadResult {
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

    companion object {
        private const val EXPECTED_RESPONSIBLE_PARTICIPANTS = 3
        private const val START_INFERENCE_MESSAGE_TYPE = "StartInference"
        private const val FINISH_INFERENCE_MESSAGE_TYPE = "FinishInference"
        private const val MISSED_INFERENCE_MESSAGE_TYPE = "MissedInference"
        private const val EXEC_FINISH_SIGN_DOMAIN = "v2_exec_finish_sig_v1"
        private const val V2_EXECUTOR_PROOF_EVENT = "v2_executor_proof"
    }
}
