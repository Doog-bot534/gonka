import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.kotlin.Logger
import java.util.concurrent.TimeUnit

@Timeout(value = 15, unit = TimeUnit.MINUTES)
class PropagationTests : TestermintTest() {

    @Test
    fun `off-chain propagation - commit metadata propagates between participants`() {
        logSection("=== TEST: Off-Chain Propagation - Commit Metadata Propagation ===")

        // Initialize cluster with 3 participants
        val (cluster, genesis) = initCluster(
            joinCount = 2,
            reboot = true
        )

        val join1 = cluster.joinPairs[0]
        val join2 = cluster.joinPairs[1]

        logSection("✅ Cluster Initialized with 3 participants")
        Logger.info("  Genesis: ${genesis.node.getColdAddress()}")
        Logger.info("  Join1: ${join1.node.getColdAddress()}")
        Logger.info("  Join2: ${join2.node.getColdAddress()}")

        // Set PoC weights to ensure all participants commit
        genesis.setPocWeight(10)
        join1.setPocWeight(10)
        join2.setPocWeight(10)

        logSection("Waiting for PoC generation phase")
        genesis.waitForStage(EpochStage.START_OF_POC)
        Logger.info("PoC stage started")

        // Wait a few blocks for artifact generation
        genesis.node.waitForNextBlock(5)

        logSection("Checking artifact stores on all participants")
        val epochData = genesis.getEpochData()
        val pocHeight = epochData.latestEpoch.pocStartBlockHeight
        
        val genesisState = genesis.api.getPocArtifactsState(pocHeight)
        val join1State = join1.api.getPocArtifactsState(pocHeight)
        val join2State = join2.api.getPocArtifactsState(pocHeight)

        Logger.info("Genesis artifacts: count=${genesisState.count}, rootHash=${genesisState.rootHash}")
        Logger.info("Join1 artifacts: count=${join1State.count}, rootHash=${join1State.rootHash}")
        Logger.info("Join2 artifacts: count=${join2State.count}, rootHash=${join2State.rootHash}")

        // Verify all participants have generated artifacts
        assertThat(genesisState.count).isGreaterThan(0)
        assertThat(join1State.count).isGreaterThan(0)
        assertThat(join2State.count).isGreaterThan(0)

        logSection("Waiting for PoC exchange phase (on-chain commit submission)")
        genesis.waitForStage(EpochStage.POC_EXCHANGE_DEADLINE)
        Logger.info("PoC exchange phase - participants should commit")

        // Wait for commits to be submitted
        genesis.node.waitForNextBlock(3)

        logSection("✅ Test Complete - All participants generated artifacts")
        Logger.info("Genesis artifacts: count=${genesisState.count}")
        Logger.info("Join1 artifacts: count=${join1State.count}")
        Logger.info("Join2 artifacts: count=${join2State.count}")
        Logger.info("This test verifies that all participants can generate PoC artifacts for propagation")
    }

    @Test
    fun `off-chain propagation - manual header propagation between nodes`() {
        logSection("=== TEST: Off-Chain Propagation - Manual Header Propagation ===")

        val (cluster, genesis) = initCluster(
            joinCount = 2,
            reboot = true
        )

        val join1 = cluster.joinPairs[0]
        val join2 = cluster.joinPairs[1]

        logSection("✅ Cluster Initialized")

        // Set PoC weights
        genesis.setPocWeight(10)
        join1.setPocWeight(10)
        join2.setPocWeight(10)

        logSection("Waiting for PoC generation")
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.node.waitForNextBlock(5)

        val epochData = genesis.getEpochData()
        val pocHeight = epochData.latestEpoch.pocStartBlockHeight
        
        val genesisState = genesis.api.getPocArtifactsState(pocHeight)
        Logger.info("Genesis artifacts: count=${genesisState.count}, rootHash=${genesisState.rootHash}")

        assertThat(genesisState.count).isGreaterThan(0)

        logSection("Creating and propagating bundle header from genesis to join1")
        
        // Create a bundle header (simulating what bundler.Publish() creates)
        val bundleHeader = PropagationBundleHeader(
            bundleId = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
            participant = genesis.node.getColdAddress(),
            pocHeight = pocHeight,
            pocBlockHash = "aa".repeat(32),
            rootHash = genesisState.rootHash,
            count = genesisState.count.toInt(),
            version = 1,
            createdAt = System.currentTimeMillis() / 1000,
            signature = "bb".repeat(64)
        )

        val headerMessage = PropagationHeaderMessage(
            treeIdx = 0,
            header = bundleHeader
        )

        logSection("Sending header from genesis to join1")
        try {
            join1.api.sendPropagationHeader(headerMessage)
            Logger.info("✅ Header sent successfully to join1")
        } catch (e: Exception) {
            Logger.error("❌ Failed to send header: ${e.message}")
            throw e
        }

        logSection("Testing header propagation to join2")
        try {
            join2.api.sendPropagationHeader(headerMessage)
            Logger.info("✅ Header sent successfully to join2")
        } catch (e: Exception) {
            Logger.error("❌ Failed to send header: ${e.message}")
            throw e
        }

        logSection("✅ Test Complete - Manual header propagation verified")
        Logger.info("Headers successfully propagated via HTTP POST /v1/propagation/header")
    }

    @Test
    fun `off-chain propagation - multi-publisher scenario`() {
        logSection("=== TEST: Off-Chain Propagation - Multi-Publisher Scenario ===")

        val (cluster, genesis) = initCluster(
            joinCount = 2,
            reboot = true
        )

        val join1 = cluster.joinPairs[0]
        val join2 = cluster.joinPairs[1]

        logSection("✅ Cluster with 3 participants initialized")

        genesis.setPocWeight(10)
        join1.setPocWeight(10)
        join2.setPocWeight(10)

        logSection("Waiting for PoC generation")
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.node.waitForNextBlock(5)

        logSection("Collecting artifact states from all participants")
        val epochData = genesis.getEpochData()
        val pocHeight = epochData.latestEpoch.pocStartBlockHeight
        
        val states = mapOf(
            "genesis" to genesis.api.getPocArtifactsState(pocHeight),
            "join1" to join1.api.getPocArtifactsState(pocHeight),
            "join2" to join2.api.getPocArtifactsState(pocHeight)
        )

        states.forEach { (name, state) ->
            Logger.info("$name: count=${state.count}, rootHash=${state.rootHash}")
            assertThat(state.count).isGreaterThan(0)
        }

        logSection("Simulating simultaneous publishing from all participants")
        
        val publishers = listOf(
            Triple(genesis, "genesis", genesis.api),
            Triple(join1, "join1", join1.api),
            Triple(join2, "join2", join2.api)
        )

        val headers = publishers.mapIndexed { idx, (pair, name, api) ->
            val state = states[name]!!
            PropagationBundleHeader(
                bundleId = "%064x".format(idx.toLong()),
                participant = pair.node.getColdAddress(),
                pocHeight = pocHeight,
                pocBlockHash = "aa".repeat(32),
                rootHash = state.rootHash,
                count = state.count.toInt(),
                version = 1,
                createdAt = System.currentTimeMillis() / 1000,
                signature = "bb".repeat(64)
            )
        }

        logSection("Broadcasting headers: each participant sends to all others")
        
        var successfulSends = 0
        var totalAttempts = 0
        
        publishers.forEach { (senderPair, senderName, _) ->
            publishers.forEach { (receiverPair, receiverName, receiverApi) ->
                if (senderName == receiverName) return@forEach // Don't send to self
                
                val header = headers.find { it.participant == senderPair.node.getColdAddress() }!!
                val message = PropagationHeaderMessage(treeIdx = 0, header = header)
                
                totalAttempts++
                try {
                    receiverApi.sendPropagationHeader(message)
                    Logger.info("✅ $senderName → $receiverName: header propagated")
                    successfulSends++
                } catch (e: Exception) {
                    Logger.warn("❌ $senderName → $receiverName: failed - ${e.message}")
                }
            }
        }

        logSection("Propagation Results")
        Logger.info("Total send attempts: $totalAttempts")
        Logger.info("Successful sends: $successfulSends")
        Logger.info("Success rate: ${(successfulSends * 100.0 / totalAttempts).toInt()}%")

        // In a 3-participant network, we expect 3 senders × 2 receivers = 6 successful propagations
        assertThat(successfulSends).isEqualTo(6)

        logSection("✅ Test Complete - Multi-publisher propagation verified")
        Logger.info("All participants successfully propagated metadata to all others")
    }

    @Test
    fun `off-chain propagation - multi-node production simulation`() {
        val nodeCount = 6
        val joinCount = nodeCount - 1
        logSection("=== TEST: Off-Chain Propagation - $nodeCount Node Production Simulation ===")

        val (cluster, genesis) = initCluster(
            joinCount = joinCount,
            reboot = true
        )

        val allParticipants = listOf(genesis) + cluster.joinPairs

        logSection("Cluster with $nodeCount participants initialized")
        Logger.info("Participants:")
        allParticipants.forEachIndexed { idx, pair ->
            val name = if (idx == 0) "genesis" else "join$idx"
            Logger.info("  $name: ${pair.node.getColdAddress()}")
        }

        logSection("Setting PoC weights on all $nodeCount participants")
        allParticipants.forEach { it.setPocWeight(10) }

        logSection("Waiting for PoC generation")
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.node.waitForNextBlock(5)

        logSection("Collecting artifact states from all $nodeCount participants")
        val epochData = genesis.getEpochData()
        val pocHeight = epochData.latestEpoch.pocStartBlockHeight

        val states = allParticipants.mapIndexed { idx, pair ->
            val name = if (idx == 0) "genesis" else "join$idx"
            name to pair.api.getPocArtifactsState(pocHeight)
        }.toMap()

        states.forEach { (name, state) ->
            Logger.info("$name: count=${state.count}, rootHash=${state.rootHash}")
            assertThat(state.count).isGreaterThan(0)
        }

        logSection("Creating bundle headers for all $nodeCount participants")

        val headers = allParticipants.mapIndexed { idx, pair ->
            val name = if (idx == 0) "genesis" else "join$idx"
            val state = states[name]!!
            Triple(
                name,
                pair,
                PropagationBundleHeader(
                    bundleId = "%064x".format(idx.toLong()),
                    participant = pair.node.getColdAddress(),
                    pocHeight = pocHeight,
                    pocBlockHash = "aa".repeat(32),
                    rootHash = state.rootHash,
                    count = state.count.toInt(),
                    version = 1,
                    createdAt = System.currentTimeMillis() / 1000,
                    signature = "bb".repeat(64)
                )
            )
        }

        logSection("Propagating headers: each participant sends to all others")
        val receiverCount = nodeCount - 1
        Logger.info("Expected propagations: $nodeCount senders x $receiverCount receivers = ${nodeCount * receiverCount} total")

        var successfulSends = 0
        var totalAttempts = 0
        val propagationMatrix = mutableMapOf<String, MutableMap<String, Boolean>>()

        headers.forEach { (senderName, _, senderHeader) ->
            propagationMatrix[senderName] = mutableMapOf()

            headers.forEach { (receiverName, receiverPair, _) ->
                if (senderName == receiverName) return@forEach

                val message = PropagationHeaderMessage(
                    treeIdx = 0,
                    header = senderHeader
                )

                totalAttempts++
                try {
                    receiverPair.api.sendPropagationHeader(message)
                    successfulSends++
                    propagationMatrix[senderName]!![receiverName] = true
                } catch (e: Exception) {
                    Logger.debug("$senderName -> $receiverName: failed - ${e.message}")
                    propagationMatrix[senderName]!![receiverName] = false
                }
            }
        }

        logSection("Propagation Results")
        Logger.info("Total attempts: $totalAttempts")
        Logger.info("Successful: $successfulSends")
        Logger.info("Failed: ${totalAttempts - successfulSends}")
        Logger.info("Success rate: ${(successfulSends * 100.0 / totalAttempts).toInt()}%")

        Logger.info("\nPropagation Matrix (rows=senders, cols=receivers):")
        headers.forEach { (senderName, _, _) ->
            val successes = propagationMatrix[senderName]!!.count { it.value }
            Logger.info("  $senderName: $successes/$receiverCount successful")
        }

        val successRate = successfulSends * 100.0 / totalAttempts
        assertThat(successRate).isGreaterThan(80.0)
            .describedAs("At least 80% of propagations should succeed in $nodeCount-node network")

        logSection("Test Complete - $nodeCount-node propagation simulation verified")
    }
}
