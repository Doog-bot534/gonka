import com.productscience.*
import com.productscience.data.*
import kotlin.test.assertNotNull
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.runBlocking
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import java.time.Duration
import java.util.concurrent.Executors
import kotlin.test.assertNotNull

/**
 * Mirror of DevshardTests but routed through versiond -> devshardd instead of
 * dapi's in-process HostManager. The shape of every assertion is the same;
 * only the test setup differs:
 *
 *  - docker-compose.versiond.yml is included for every pair so each pair runs
 *    a versiond container that boots the locally-built devshardd binary as
 *    version "dev" (via VERSIOND_OVERRIDE_dev + VERSIOND_FORCE).
 *  - VERSIOND_SERVICE_NAME=versiond is exported so each pair's proxy emits a
 *    /devshard/ -> versiond_backend location.
 *  - startDevshardProxy is launched with routePrefix="/devshard/dev" so
 *    devshardctl builds host URLs as proxy/devshard/dev/sessions/:id/...
 *    nginx strips /devshard/, versiond strips /dev/, devshardd handles
 *    /sessions/:id/...
 *  - DAPI's in-process HostManager is still mounted on /v1/devshard for the
 *    legacy path; the new test does not exercise it.
 */
class DevshardStandaloneTests : TestermintTest() {
    // The default join count is 2 -> three pairs total (genesis, join1, join2).
    // Every pair gets the versiond compose extension so each runs its own
    // devshardd child managed by its own ${KEY_NAME}-versiond container.
    private val standaloneVersiondFiles = listOf(GENESIS_KEY_NAME, "join1", "join2")
        .associateWith { listOf("docker-compose.versiond.yml") }

    // Switches the test cluster from "default" to "devshardd via versiond":
    //  - VERSIOND_BINARY_NAME selects the binary versiond launches per child
    //  - VERSIOND_OVERRIDE_dev points at the bind-mounted host binary
    //  - VERSIOND_FORCE makes versiond run that version even though it is
    //    not in the chain's approved_versions list
    //  - VERSIOND_SERVICE_NAME enables the proxy's /devshard/ -> versiond
    //    upstream block
    private val standaloneEnv = mapOf(
        "VERSIOND_BINARY_NAME" to "devshardd",
        "VERSIOND_OVERRIDE_dev" to "/opt/overrides/devshardd",
        "VERSIOND_FORCE" to "dev",
        "VERSIOND_SERVICE_NAME" to "versiond",
    )

    private val devshardRoutePrefix = "/devshard/dev"

    private val standaloneConfig = inferenceConfig.copy(
        genesisSpec = inferenceConfig.genesisSpec?.merge(devshardNoRestrictionsSpec) ?: devshardNoRestrictionsSpec,
        additionalDockerFilesByKeyName = standaloneVersiondFiles,
        additionalEnvVars = standaloneEnv,
    )

    private val standaloneLongEpochConfig = inferenceConfig.copy(
        genesisSpec = createSpec(epochLength = 40, epochShift = 10).merge(devshardNoRestrictionsSpec),
        additionalDockerFilesByKeyName = standaloneVersiondFiles,
        additionalEnvVars = standaloneEnv,
    )

    private val standaloneAlwaysValidateConfig = inferenceConfig.copy(
        genesisSpec = inferenceConfig.genesisSpec
            ?.merge(devshardNoRestrictionsSpec)
            ?.merge(devshardAlwaysValidateSpec)
            ?: devshardNoRestrictionsSpec.merge(devshardAlwaysValidateSpec),
        additionalDockerFilesByKeyName = standaloneVersiondFiles,
        additionalEnvVars = standaloneEnv,
    )

    @Test
    fun `create devshard escrow and query it`() {
        val (cluster, genesis) = initCluster(config = standaloneConfig, reboot = true)
        genesis.waitForNextEpoch()

        val creator = genesis.node.getColdAddress()
        val initialBalance = genesis.getBalance(creator)

        logSection("Creating devshard escrow")
        val escrowAmount = 7_000_000_000L
        val txResponse = genesis.createDevshardEscrow(escrowAmount)
        assertThat(txResponse.code).isEqualTo(0)

        logSection("Querying devshard escrow")
        val escrowResponse = genesis.node.queryDevshardEscrow(1)
        assertThat(escrowResponse.found).isTrue()
        assertThat(escrowResponse.escrow).isNotNull()
        assertThat(escrowResponse.escrow!!.creator).isEqualTo(creator)
        assertThat(escrowResponse.escrow!!.amount).isEqualTo(escrowAmount.toString())
        assertThat(escrowResponse.escrow!!.slots).hasSize(16)
        assertThat(escrowResponse.escrow!!.settled).isFalse()

        logSection("Verifying balance decreased")
        val balanceAfter = genesis.getBalance(creator)
        assertThat(balanceAfter).isEqualTo(initialBalance - escrowAmount)
    }

    @Test
    fun `devshard inference e2e with settlement via devshardd`() {
        val (cluster, genesis) = initCluster(config = standaloneConfig, reboot = true)
        genesis.waitForNextEpoch()

        cluster.stubDevshardChatResponse()

        val user = genesis.createFundedDevshardUser("devshardd-user")

        genesis.waitForNextInferenceWindow()

        val escrowAmount = 7_000_000_000L
        val escrowId = genesis.createDevshardEscrowForUser(escrowAmount, user.keyName)

        logSection("Starting devshard proxy against devshardd")
        val handle = genesis.startDevshardProxy(
            escrowId = escrowId,
            keyName = user.keyName,
            routePrefix = devshardRoutePrefix,
        )

        try {
            genesis.waitForDevshardProxyWarmup()
            logSection("Sending chat completions via proxy")
            for (i in 0 until 20) {
                val response = genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "test prompt $i")
                assertThat(response).isNotEmpty()
            }

            genesis.assertDevshardSettlement(handle, escrowId, user, escrowAmount, requireCompletedValidations = false)
        } finally {
            genesis.stopDevshardProxy(escrowId)
        }
    }

    @Test
    fun `devshard streaming inference e2e with settlement via devshardd`() {
        val (cluster, genesis) = initCluster(config = standaloneConfig, reboot = true)
        genesis.waitForNextEpoch()

        cluster.stubDevshardChatResponse(content = "hello from stream", streamDelay = Duration.ofMillis(50))

        val user = genesis.createFundedDevshardUser("devshardd-stream-user")

        genesis.waitForNextInferenceWindow()

        val escrowAmount = 7_000_000_000L
        val escrowId = genesis.createDevshardEscrowForUser(escrowAmount, user.keyName)

        logSection("Starting devshard proxy against devshardd")
        val handle = genesis.startDevshardProxy(
            escrowId = escrowId,
            keyName = user.keyName,
            routePrefix = devshardRoutePrefix,
        )

        try {
            genesis.waitForDevshardProxyWarmup()
            logSection("Sending streaming chat completions via proxy")
            val numInferences = 20L
            for (i in 0 until numInferences) {
                val response = genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "test prompt $i", stream = true)
                assertThat(response).isNotEmpty()
                assertThat(response).contains("data:")
            }

            genesis.assertDevshardSettlement(handle, escrowId, user, escrowAmount, requireCompletedValidations = false)

            logSection("Verifying inference statuses")
            for (inferenceId in 1..numInferences) {
                val inference = cosmosJson.fromJson(
                    genesis.getDevshardInferenceState(handle.proxyUrl, inferenceId),
                    DevshardInferencePayload::class.java,
                )
                logSection("Inference $inferenceId: $inference")
                assertNotNull(inference)
                assertThat(inference.status).isEqualTo(DevshardInferenceStatus.FINISHED)
            }
        } finally {
            genesis.stopDevshardProxy(escrowId)
        }
    }

    @Test
    fun `parallel devshard sessions with isolated settlement via devshardd`() {
        val sessionCount = 6
        val (cluster, genesis) = initCluster(config = standaloneLongEpochConfig, reboot = true)
        genesis.waitForNextEpoch()

        cluster.stubDevshardChatResponse()

        data class UserInfo(val keyName: String, val address: String)
        data class SessionSetup(val keyName: String, val address: String, val escrowId: Long)

        val fundAmount = 10_000_000_000L
        val escrowAmount = 7_000_000_000L

        val users = (0 until sessionCount).map { i ->
            val user = genesis.createFundedDevshardUser("devshardd-parallel-$i", fundAmount)
            UserInfo(user.keyName, user.address)
        }

        genesis.waitForNextEpoch()
        genesis.waitForNextInferenceWindow()

        val sessions = users.mapIndexed { i, user ->
            logSection("Creating escrow for user $i")
            val escrowId = genesis.createDevshardEscrowForUser(escrowAmount, user.keyName)
            SessionSetup(user.keyName, user.address, escrowId)
        }

        logSection("Starting $sessionCount devshard proxies against devshardd")
        val handles = sessions.map { session ->
            genesis.startDevshardProxy(
                escrowId = session.escrowId,
                keyName = session.keyName,
                routePrefix = devshardRoutePrefix,
            )
        }

        try {
            genesis.waitForDevshardProxyWarmup()
            logSection("Running $sessionCount proxy sessions in parallel")
            val dispatcher = Executors.newFixedThreadPool(sessionCount).asCoroutineDispatcher()
            runBlocking(dispatcher) {
                handles.map { handle ->
                    async {
                        for (i in 0 until 10) {
                            genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "test prompt $i")
                        }
                    }
                }.awaitAll()
            }
            runBlocking(dispatcher) {
                handles.map { handle ->
                    async {
                        for (i in 0 until 10) {
                            genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "test prompt $i")
                        }
                    }
                }.awaitAll()
            }

            logSection("Finalizing, settling, and verifying $sessionCount escrows")
            sessions.zip(handles).forEach { (session, handle) ->
                val result = genesis.finalizeDevshardProxy(handle.proxyUrl)
                assertThat(result.parsed.escrowId)
                    .withFailMessage("Escrow ID mismatch for ${session.keyName}")
                    .isEqualTo(session.escrowId.toString())
                assertThat(result.parsed.hostStats).isNotEmpty()
                assertThat(result.parsed.signatures).isNotEmpty()
                assertThat(result.parsed.hostStats.sumOf { it.completedValidations }).isGreaterThan(0)

                val settleResp = genesis.settleDevshardEscrow(result.rawJson, from = session.keyName)
                assertThat(settleResp.code)
                    .withFailMessage("Settlement failed for escrow ${session.escrowId}")
                    .isEqualTo(0)

                val escrow = genesis.node.queryDevshardEscrow(session.escrowId)
                assertThat(escrow.escrow!!.settled)
                    .withFailMessage("Escrow ${session.escrowId} not settled")
                    .isTrue()

                val balance = genesis.getBalance(session.address)
                assertThat(balance)
                    .withFailMessage("User ${session.keyName} did not receive refund")
                    .isGreaterThan(fundAmount - escrowAmount)
            }
        } finally {
            handles.forEach { genesis.stopDevshardProxy(it.escrowId) }
        }
    }

    @Test
    fun `invalid inference is challenged via devshardd`() {
        val (cluster, genesis) = initCluster(config = standaloneAlwaysValidateConfig, reboot = true)
        genesis.waitForNextEpoch()

        cluster.allPairs.forEach { pair ->
            pair.mock?.stubDevshardResponseForAllSegments(
                response = defaultInferenceResponseObject,
                streamDelay = Duration.ofMillis(50),
            )
        }
        cluster.allPairs.last().mock?.stubDevshardResponseForAllSegments(
            response = defaultInferenceResponseObject.withMissingLogit(),
        )

        val user = genesis.createFundedDevshardUser("devshardd-challenged-user")

        genesis.waitForNextInferenceWindow()

        val escrowAmount = 7_000_000_000L
        val escrowId = genesis.createDevshardEscrowForUser(escrowAmount, user.keyName)

        logSection("Starting devshard proxy against devshardd")
        val handle = genesis.startDevshardProxy(
            escrowId = escrowId,
            keyName = user.keyName,
            routePrefix = devshardRoutePrefix,
        )

        try {
            genesis.waitForDevshardProxyWarmup()
            logSection("Sending chat completions via proxy")
            val numInferences = 20L
            for (i in 0 until numInferences) {
                val response = genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "test prompt $i")
                assertThat(response).isNotEmpty()
            }

            genesis.waitForDevshardPreFinalize()
            logSection("Finalizing via proxy")
            val result = genesis.finalizeDevshardProxy(handle.proxyUrl)

            logSection("Verifying settlement data")
            assertThat(result.parsed.escrowId).isEqualTo("$escrowId")
            assertThat(result.parsed.nonce).isGreaterThan(0)
            assertThat(result.parsed.hostStats).isNotEmpty()
            assertThat(result.parsed.signatures).isNotEmpty()

            logSection("Submitting settlement from user account")
            val settleResp = genesis.settleDevshardEscrow(result.rawJson, from = user.keyName)
            assertThat(settleResp.code).isEqualTo(0)

            logSection("Verifying escrow settled")
            val escrow = genesis.node.queryDevshardEscrow(escrowId)
            assertThat(escrow.escrow!!.settled).isTrue()

            logSection("Verifying inference status")
            val inference = assertNotNull(genesis.findChallengedDevshardInference(handle, numInferences))
            logSection("Inference: $inference")
            assertThat(inference.status).isEqualTo(DevshardInferenceStatus.CHALLENGED)
            assertThat(inference.votesInvalid).isNotZero()
        } finally {
            genesis.stopDevshardProxy(escrowId)
        }
    }
}
