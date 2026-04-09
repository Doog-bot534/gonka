import com.github.dockerjava.core.DockerClientBuilder
import com.github.kittinunf.fuel.Fuel
import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.*
import org.tinylog.kotlin.Logger
import java.time.Duration
import java.util.concurrent.TimeUnit

/**
 * E2E tests for the full versiond → real subnetctl pipeline.
 *
 * Flow under test:
 *   1. Create user key + fund + create subnet escrow
 *   2. Write dynamic config (private key, escrow ID) into versiond container
 *   3. Governance proposal adds approved subnet version
 *   4. versiond downloads real subnetctl binary and starts it
 *   5. Chat completions routed through versiond reverse proxy → subnetctl → mock ML nodes
 *   6. Finalize + settle the escrow on-chain
 *
 * Requires docker-compose.versiond-subnet.yml (adds versiond + testsubnet-server services).
 */
@TestInstance(TestInstance.Lifecycle.PER_CLASS)
@TestMethodOrder(MethodOrderer.OrderAnnotation::class)
@Timeout(value = 10, unit = TimeUnit.MINUTES)
class VersiondSubnetTests : TestermintTest() {

    private val versiondUrl = "http://localhost:$VERSIOND_HOST_PORT"
    private val testsubnetServerUrl = "http://localhost:$TESTSUBNET_SERVER_HOST_PORT"
    private val dapiMlUrl = "http://localhost:$DAPI_ML_HOST_PORT"
    private val subnetBinaryDockerUrl = "http://${GENESIS_KEY_NAME}-testsubnet-server:8080/subnetctl.zip"

    private lateinit var cluster: LocalCluster
    private lateinit var genesis: LocalInferencePair
    private lateinit var subnetSha256: String
    private lateinit var userKeyName: String
    private lateinit var userAddress: String
    private var escrowId: Long = 0

    private val versionName = "v0.2.11"

    @BeforeAll
    fun setup() {
        val config = inferenceConfig.copy(
            additionalDockerFilesByKeyName = mapOf(
                GENESIS_KEY_NAME to listOf("docker-compose.versiond-subnet.yml")
            )
        )
        val (c, g) = initCluster(config = config, reboot = true)
        cluster = c
        genesis = g

        logSection("Configuring mock inference responses")
        cluster.allPairs.forEach { pair ->
            pair.mock?.setInferenceResponse(
                """{"id":"test-subnet","object":"chat.completion","created":0,"model":"$defaultModel","choices":[{"index":0,"message":{"role":"assistant","content":"hello from subnet e2e"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}""",
                streamDelay = Duration.ofMillis(50),
            )
        }

        logSection("Waiting for testsubnet-server readiness")
        subnetSha256 = waitForSubnetServer()
        Logger.info("subnetctl zip sha256: $subnetSha256")

        logSection("Waiting for versiond readiness")
        waitForVersiondHealthz()

        logSection("Waiting for ML nodes to reach INFERENCE state")
        waitForInferenceNodes()

        logSection("Waiting for first epoch")
        genesis.waitForNextEpoch()

        logSection("Creating subnet user account")
        userKeyName = "subnet-vd-user"
        val userKey = genesis.node.createKey(userKeyName)
        userAddress = userKey.address

        val fundAmount = 10_000_000_000L
        val transferResp = genesis.submitTransaction(
            listOf("bank", "send", genesis.node.getColdAddress(), userAddress, "${fundAmount}${genesis.config.denom}")
        )
        assertThat(transferResp.code).isEqualTo(0)

        genesis.waitForNextInferenceWindow()

        logSection("Creating subnet escrow")
        val escrowAmount = 7_000_000_000L
        val txResp = genesis.createSubnetEscrow(escrowAmount, from = userKeyName)
        assertThat(txResp.code).isEqualTo(0)
        escrowId = txResp.getEscrowId()!!
        Logger.info("Created escrow ID: $escrowId")

        logSection("Writing subnet config to versiond container")
        val privateKey = genesis.node.getPrivateKey(userKeyName).trim()
        writeSubnetEnvFile(privateKey, escrowId)

        logSection("Submitting governance proposal to add $versionName")
        val params = genesis.getParams()
        val updatedParams = params.withApprovedVersions(
            listOf(
                SubnetApprovedVersion(
                    name = versionName,
                    binary = subnetBinaryDockerUrl,
                    sha256 = subnetSha256,
                )
            )
        )
        genesis.runProposal(cluster, UpdateParams(params = updatedParams))

        logSection("Waiting for versiond to start $versionName")
        waitUntil("versiond starts $versionName", timeoutSeconds = 120) {
            getVersiondHealth().any {
                it["name"] == versionName && it["status"] == "running"
            }
        }

        logSection("Waiting for subnetctl to be ready")
        waitUntil("subnetctl /v1/status reachable", timeoutSeconds = 60) {
            try {
                val (_, resp, _) = Fuel.get("$versiondUrl/$versionName/v1/status")
                    .timeoutRead(5000)
                    .responseString()
                resp.statusCode == 200
            } catch (_: Exception) { false }
        }

        logHighlight("Setup complete: versiond running real subnetctl for $versionName")
    }

    @AfterAll
    fun teardown() {
        if (::genesis.isInitialized) {
            genesis.markNeedsReboot()
        }
    }

    @Test
    @Order(1)
    fun `subnetctl status endpoint reachable through versiond`() {
        logSection("Querying subnetctl status through versiond proxy")
        val status = getVersiondProxyJson("$versionName/v1/status")
        assertThat(status["escrow_id"]).isEqualTo(escrowId.toString())
        assertThat(status["phase"]).isEqualTo("active")
        logHighlight("subnetctl status: escrow=$escrowId phase=${status["phase"]}")
    }

    @Test
    @Order(2)
    fun `chat completion through versiond to real subnetctl`() {
        logSection("Sending chat completions through versiond → subnetctl → mock ML")
        val chatBody = """{"model":"$defaultModel","messages":[{"role":"user","content":"test prompt"}],"max_tokens":100}"""

        for (i in 0 until 5) {
            val (_, response, result) = Fuel.post("$versiondUrl/$versionName/v1/chat/completions")
                .header("Content-Type", "application/json")
                .body(chatBody)
                .timeoutRead(60_000)
                .responseString()

            assertThat(response.statusCode)
                .withFailMessage("Chat completion $i returned ${response.statusCode}: ${result}")
                .isEqualTo(200)

            val body = result.get()
            assertThat(body).contains("choices")
        }
        logHighlight("5 chat completions succeeded through full versiond → subnetctl chain")
    }

    @Test
    @Order(3)
    fun `streaming chat completion through the stack`() {
        logSection("Sending streaming chat completion")
        val chatBody = """{"model":"$defaultModel","messages":[{"role":"user","content":"stream test"}],"max_tokens":100,"stream":true}"""

        val (_, response, result) = Fuel.post("$versiondUrl/$versionName/v1/chat/completions")
            .header("Content-Type", "application/json")
            .body(chatBody)
            .timeoutRead(60_000)
            .responseString()

        assertThat(response.statusCode)
            .withFailMessage("Streaming chat returned ${response.statusCode}: ${result}")
            .isEqualTo(200)

        val body = result.get()
        assertThat(body).contains("data:")
        logHighlight("Streaming chat completion succeeded through versiond → subnetctl")
    }

    @Test
    @Order(4)
    fun `finalize and settle subnet escrow`() {
        logSection("Finalizing subnet session through versiond proxy")
        val (_, finalizeResp, finalizeResult) = Fuel.post("$versiondUrl/$versionName/v1/finalize")
            .timeoutRead(120_000)
            .responseString()

        assertThat(finalizeResp.statusCode)
            .withFailMessage("Finalize returned ${finalizeResp.statusCode}: ${finalizeResult}")
            .isEqualTo(200)

        val settlementJson = finalizeResult.get()
        assertThat(settlementJson).contains("escrow_id")
        assertThat(settlementJson).contains("host_stats")
        assertThat(settlementJson).contains("signatures")

        logSection("Submitting settlement on-chain")
        val settleResp = genesis.settleSubnetEscrow(settlementJson, from = userKeyName)
        assertThat(settleResp.code)
            .withFailMessage("Settlement tx failed: ${settleResp.rawLog}")
            .isEqualTo(0)

        logSection("Verifying escrow settled")
        val escrow = genesis.node.querySubnetEscrow(escrowId)
        assertThat(escrow.escrow!!.settled).isTrue()

        logHighlight("Subnet escrow $escrowId finalized and settled through versiond")
    }

    // ---------------------------------------------------------------------------
    // Helpers
    // ---------------------------------------------------------------------------

    private fun InferenceParams.withApprovedVersions(
        versions: List<SubnetApprovedVersion>
    ): InferenceParams {
        val escrow = this.subnetEscrowParams ?: SubnetEscrowParams(
            minAmount = 5_000_000_000,
            maxAmount = 10_000_000_000,
            maxEscrowsPerEpoch = 100,
            groupSize = 16,
            tokenPrice = 1,
        )
        return this.copy(
            subnetEscrowParams = escrow.copy(approvedVersions = versions)
        )
    }

    private fun writeSubnetEnvFile(privateKey: String, escrowId: Long) {
        val dockerClient = DockerClientBuilder.getInstance().build()
        val containerId = "${GENESIS_KEY_NAME}-versiond"
        val envContent = "SUBNET_PRIVATE_KEY=$privateKey\nSUBNET_ESCROW_ID=$escrowId\n"
        val cmd = dockerClient.execCreateCmd(containerId)
            .withAttachStdout(true)
            .withAttachStderr(true)
            .withCmd("sh", "-c", "mkdir -p /opt/versiond && cat > /opt/versiond/subnet.env << 'ENVEOF'\n${envContent}ENVEOF")
            .exec()
        dockerClient.execStartCmd(cmd.id).exec(com.productscience.ExecCaptureOutput())
            .awaitCompletion(10, TimeUnit.SECONDS)
        Logger.info("Wrote subnet.env to $containerId (escrow=$escrowId)")
    }

    private fun waitForSubnetServer(): String {
        var sha256: String? = null
        val deadline = System.currentTimeMillis() + 120_000
        while (sha256 == null && System.currentTimeMillis() < deadline) {
            try {
                val (_, _, result) = Fuel.get("$testsubnetServerUrl/subnetctl.zip.sha256")
                    .timeoutRead(5000)
                    .responseString()
                sha256 = result.get().trim()
            } catch (e: Exception) {
                Logger.debug("testsubnet-server not ready: ${e.message}")
                Thread.sleep(2000)
            }
        }
        check(sha256 != null) { "testsubnet-server did not become ready within 120s" }
        return sha256
    }

    private fun waitForVersiondHealthz() {
        val deadline = System.currentTimeMillis() + 120_000
        while (System.currentTimeMillis() < deadline) {
            try {
                val (_, response, _) = Fuel.get("$versiondUrl/healthz")
                    .timeoutRead(5000)
                    .responseString()
                if (response.statusCode == 200) return
            } catch (e: Exception) {
                Logger.debug("versiond not ready: ${e.message}")
            }
            Thread.sleep(2000)
        }
        error("versiond /healthz did not become ready within 120s")
    }

    private fun waitForInferenceNodes() {
        val deadline = System.currentTimeMillis() + 120_000
        while (System.currentTimeMillis() < deadline) {
            try {
                val nodes = genesis.api.getNodes()
                if (nodes.isNotEmpty() && nodes.all { it.state.currentStatus == "INFERENCE" }) {
                    Logger.info("All ${nodes.size} ML nodes in INFERENCE state")
                    return
                }
            } catch (e: Exception) {
                Logger.debug("Waiting for INFERENCE nodes: ${e.message}")
            }
            Thread.sleep(5000)
        }
        error("ML nodes did not reach INFERENCE state within 120s")
    }

    @Suppress("UNCHECKED_CAST")
    private fun getVersiondHealth(): List<Map<String, Any>> {
        return try {
            val (_, _, result) = Fuel.get("$versiondUrl/healthz")
                .timeoutRead(5000)
                .responseString()
            cosmosJson.fromJson(result.get(), List::class.java) as? List<Map<String, Any>> ?: emptyList()
        } catch (e: Exception) {
            Logger.warn("Failed to query versiond /healthz: ${e.message}")
            emptyList()
        }
    }

    @Suppress("UNCHECKED_CAST")
    private fun getVersiondProxyJson(path: String): Map<String, Any> {
        val (_, response, result) = Fuel.get("$versiondUrl/$path")
            .timeoutRead(10_000)
            .responseString()
        assertThat(response.statusCode)
            .withFailMessage("GET /$path returned ${response.statusCode}: ${result}")
            .isEqualTo(200)
        return cosmosJson.fromJson(result.get(), Map::class.java) as Map<String, Any>
    }

    private fun waitUntil(description: String, timeoutSeconds: Int, condition: () -> Boolean) {
        val deadline = System.currentTimeMillis() + timeoutSeconds * 1000L
        while (System.currentTimeMillis() < deadline) {
            if (condition()) return
            Thread.sleep(2000)
        }
        error("Timed out waiting for: $description (${timeoutSeconds}s)")
    }

    companion object {
        const val VERSIOND_HOST_PORT = 7080
        const val TESTSUBNET_SERVER_HOST_PORT = 7090
        const val DAPI_ML_HOST_PORT = 9001
    }
}
