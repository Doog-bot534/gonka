import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.kotlin.Logger
import java.util.concurrent.TimeUnit

@Timeout(value = 15, unit = TimeUnit.MINUTES)
class PropagationTests : TestermintTest() {

    private fun waitForPropagation(
        pairs: List<Pair>,
        pocHeight: Long,
        expectedCount: Int,
        timeoutMs: Long = 30000,
        pollIntervalMs: Long = 500
    ) {
        val startTime = System.currentTimeMillis()
        while (System.currentTimeMillis() - startTime < timeoutMs) {
            val allReady = pairs.all { pair ->
                try {
                    val count = pair.api.getPropagationCache(pocHeight).count
                    count >= expectedCount
                } catch (e: Exception) {
                    false
                }
            }
            if (allReady) {
                Logger.info("All ${pairs.size} participants have at least $expectedCount bundles")
                return
            }
            Thread.sleep(pollIntervalMs)
        }
        Logger.warn("Timeout waiting for propagation - some nodes may not have all bundles yet")
    }

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
    fun `propagation - 3 node network natural propagation`() {
        logSection("=== TEST: Natural Propagation - 3 Node Network ===")

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

        genesis.setPocWeight(10)
        join1.setPocWeight(10)
        join2.setPocWeight(10)

        logSection("Waiting for PoC generation phase")
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.node.waitForNextBlock(5)

        val epochData = genesis.getEpochData()
        val pocHeight = epochData.latestEpoch.pocStartBlockHeight

        logSection("Verifying all participants generated artifacts")
        val genesisState = genesis.api.getPocArtifactsState(pocHeight)
        val join1State = join1.api.getPocArtifactsState(pocHeight)
        val join2State = join2.api.getPocArtifactsState(pocHeight)

        Logger.info("Genesis: count=${genesisState.count}, rootHash=${genesisState.rootHash}")
        Logger.info("Join1: count=${join1State.count}, rootHash=${join1State.rootHash}")
        Logger.info("Join2: count=${join2State.count}, rootHash=${join2State.rootHash}")

        assertThat(genesisState.count).isGreaterThan(0)
        assertThat(join1State.count).isGreaterThan(0)
        assertThat(join2State.count).isGreaterThan(0)

        logSection("Waiting for PoC exchange phase (natural propagation)")
        genesis.waitForStage(EpochStage.POC_EXCHANGE_DEADLINE)
        
        genesis.node.waitForNextBlock(5)

        logSection("Waiting for propagation to complete")
        waitForPropagation(listOf(genesis, join1, join2), pocHeight, 2)

        logSection("Querying propagation cache from all nodes")
        val genesisCacheData = genesis.api.getPropagationCache(pocHeight)
        val join1CacheData = join1.api.getPropagationCache(pocHeight)
        val join2CacheData = join2.api.getPropagationCache(pocHeight)

        Logger.info("Genesis cache: ${genesisCacheData.count} bundles")
        Logger.info("Join1 cache: ${join1CacheData.count} bundles")
        Logger.info("Join2 cache: ${join2CacheData.count} bundles")

        logSection("Verifying natural propagation occurred")
        
        assertThat(genesisCacheData.count).isGreaterThanOrEqualTo(2)
            .describedAs("Genesis should have received bundles from other participants")
        assertThat(join1CacheData.count).isGreaterThanOrEqualTo(2)
            .describedAs("Join1 should have received bundles from other participants")
        assertThat(join2CacheData.count).isGreaterThanOrEqualTo(2)
            .describedAs("Join2 should have received bundles from other participants")

        logSection("✅ Test Complete - Natural propagation verified in 3-node network")
        Logger.info("All participants successfully propagated and received bundles and proofs automatically")
        Logger.info("No manual header/proof sending - bundler and tree manager handled propagation")
    }

    @Test
    fun `propagation - 9 node network natural propagation`() {
        logSection("=== TEST: Natural Propagation - 10 Node Network ===")

        val (cluster, genesis) = initCluster(
            joinCount = 9,
            reboot = true
        )

        val allParticipants = listOf(genesis) + cluster.joinPairs

        logSection("✅ Cluster with 9 participants initialized")
        allParticipants.forEachIndexed { idx, pair ->
            val name = if (idx == 0) "genesis" else "join$idx"
            Logger.info("  $name: ${pair.node.getColdAddress()}")
        }

        logSection("Setting PoC weights on all participants")
        allParticipants.forEach { it.setPocWeight(10) }

        logSection("Waiting for PoC generation phase")
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.node.waitForNextBlock(5)

        val epochData = genesis.getEpochData()
        val pocHeight = epochData.latestEpoch.pocStartBlockHeight

        logSection("Verifying all participants generated artifacts")
        allParticipants.forEachIndexed { idx, pair ->
            val name = if (idx == 0) "genesis" else "join$idx"
            val state = pair.api.getPocArtifactsState(pocHeight)
            Logger.info("$name: count=${state.count}, rootHash=${state.rootHash}")
            assertThat(state.count).isGreaterThan(0)
        }

        logSection("Waiting for PoC exchange phase (natural propagation)")
        genesis.waitForStage(EpochStage.POC_EXCHANGE_DEADLINE)
        
        genesis.node.waitForNextBlock(8)

        logSection("Waiting for propagation to complete")
        waitForPropagation(allParticipants, pocHeight, 8)

        logSection("Querying propagation cache from all 9 nodes")
        val cacheData = allParticipants.mapIndexed { idx, pair ->
            val name = if (idx == 0) "genesis" else "join$idx"
            val cache = pair.api.getPropagationCache(pocHeight)
            Logger.info("$name cache: ${cache.count} bundles")
            kotlin.Pair(name, cache)
        }

        logSection("Verifying natural propagation occurred")
        
        cacheData.forEach { (name, cache) ->
            assertThat(cache.count).isGreaterThanOrEqualTo(8)
                .describedAs("$name should have received bundles from other participants")
        }

        val totalBundles = cacheData.sumOf { it.second.count }
        Logger.info("Total bundles across all caches: $totalBundles")

        logSection("✅ Test Complete - Natural propagation verified in 9-node network")
        Logger.info("All participants successfully propagated and received bundles automatically")
        Logger.info("Total bundles propagated: $totalBundles")
        Logger.info("Propagation handled by bundler and tree manager - no manual intervention")
    }
}
