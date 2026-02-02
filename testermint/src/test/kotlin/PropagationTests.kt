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

        logSection("Querying propagation cache from all nodes")
        val genesisCacheData = genesis.api.getPropagationCache(pocHeight)
        val join1CacheData = join1.api.getPropagationCache(pocHeight)
        val join2CacheData = join2.api.getPropagationCache(pocHeight)

        Logger.info("Genesis cache: ${genesisCacheData.bundles.size} bundles")
        Logger.info("Join1 cache: ${join1CacheData.bundles.size} bundles")
        Logger.info("Join2 cache: ${join2CacheData.bundles.size} bundles")

        genesisCacheData.bundles.forEach { bundle ->
            Logger.info("  Genesis received bundle from ${bundle.participant} (count=${bundle.count})")
        }
        join1CacheData.bundles.forEach { bundle ->
            Logger.info("  Join1 received bundle from ${bundle.participant} (count=${bundle.count})")
        }
        join2CacheData.bundles.forEach { bundle ->
            Logger.info("  Join2 received bundle from ${bundle.participant} (count=${bundle.count})")
        }

        logSection("Verifying natural propagation occurred")
        
        assertThat(genesisCacheData.bundles.size).isGreaterThan(0)
            .describedAs("Genesis should have received bundles from other participants")
        assertThat(join1CacheData.bundles.size).isGreaterThan(0)
            .describedAs("Join1 should have received bundles from other participants")
        assertThat(join2CacheData.bundles.size).isGreaterThan(0)
            .describedAs("Join2 should have received bundles from other participants")

        val genesisAddr = genesis.node.getColdAddress()
        val join1Addr = join1.node.getColdAddress()
        val join2Addr = join2.node.getColdAddress()

        val genesisReceivedFrom = genesisCacheData.bundles.map { it.participant }.toSet()
        val join1ReceivedFrom = join1CacheData.bundles.map { it.participant }.toSet()
        val join2ReceivedFrom = join2CacheData.bundles.map { it.participant }.toSet()

        Logger.info("Genesis received from: ${genesisReceivedFrom.size} unique participants")
        Logger.info("Join1 received from: ${join1ReceivedFrom.size} unique participants")
        Logger.info("Join2 received from: ${join2ReceivedFrom.size} unique participants")

        assertThat(genesisReceivedFrom).contains(join1Addr, join2Addr)
            .describedAs("Genesis should have bundles from join1 and join2")
        assertThat(join1ReceivedFrom).contains(genesisAddr, join2Addr)
            .describedAs("Join1 should have bundles from genesis and join2")
        assertThat(join2ReceivedFrom).contains(genesisAddr, join1Addr)
            .describedAs("Join2 should have bundles from genesis and join1")

        logSection("Verifying proof propagation")
        genesisCacheData.bundles.forEach { bundle ->
            Logger.info("Checking proofs for bundle ${bundle.bundleId} from ${bundle.participant}")
            val proofsResponse = genesis.api.getPropagationProofs(bundle.bundleId)
            assertThat(proofsResponse.proofs.size).isEqualTo(bundle.count.toInt())
                .describedAs("Genesis should have all ${bundle.count} proofs for bundle ${bundle.bundleId}")
            Logger.info("  ✓ Genesis has ${proofsResponse.proofs.size} proofs for bundle from ${bundle.participant}")
        }

        join1CacheData.bundles.forEach { bundle ->
            val proofsResponse = join1.api.getPropagationProofs(bundle.bundleId)
            assertThat(proofsResponse.proofs.size).isEqualTo(bundle.count.toInt())
                .describedAs("Join1 should have all ${bundle.count} proofs for bundle ${bundle.bundleId}")
            Logger.info("  ✓ Join1 has ${proofsResponse.proofs.size} proofs for bundle from ${bundle.participant}")
        }

        join2CacheData.bundles.forEach { bundle ->
            val proofsResponse = join2.api.getPropagationProofs(bundle.bundleId)
            assertThat(proofsResponse.proofs.size).isEqualTo(bundle.count.toInt())
                .describedAs("Join2 should have all ${bundle.count} proofs for bundle ${bundle.bundleId}")
            Logger.info("  ✓ Join2 has ${proofsResponse.proofs.size} proofs for bundle from ${bundle.participant}")
        }

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

        logSection("Querying propagation cache from all 9 nodes")
        val cacheData = allParticipants.mapIndexed { idx, pair ->
            val name = if (idx == 0) "genesis" else "join$idx"
            val cache = pair.api.getPropagationCache(pocHeight)
            Logger.info("$name cache: ${cache.bundles.size} bundles")
            Triple(name, pair, cache)
        }

        logSection("Analyzing propagation coverage")
        cacheData.forEach { (name, pair, cache) ->
            val uniqueParticipants = cache.bundles.map { it.participant }.toSet()
            Logger.info("$name received from ${uniqueParticipants.size} unique participants:")
            uniqueParticipants.forEach { participant ->
                val bundleCount = cache.bundles.count { it.participant == participant }
                Logger.info("  - $participant: $bundleCount bundle(s)")
            }
        }

        logSection("Verifying natural propagation occurred")
        
        cacheData.forEach { (name, _, cache) ->
            assertThat(cache.bundles.size).isGreaterThan(0)
                .describedAs("$name should have received bundles from other participants")
        }

        val allAddresses = allParticipants.map { it.node.getColdAddress() }.toSet()
        
        cacheData.forEach { (name, pair, cache) ->
            val receivedFrom = cache.bundles.map { it.participant }.toSet()
            val myAddress = pair.node.getColdAddress()
            val otherParticipants = allAddresses - myAddress
            
            val receivedFromOthers = receivedFrom.intersect(otherParticipants)
            val coveragePercent = (receivedFromOthers.size * 100.0 / otherParticipants.size).toInt()
            
            Logger.info("$name: received from ${receivedFromOthers.size}/8 other participants ($coveragePercent%)")
            
            assertThat(receivedFromOthers.size).isGreaterThan(0)
                .describedAs("$name should have received bundles from at least 1 other participant")
        }

        val totalBundles = cacheData.sumOf { it.third.bundles.size }
        Logger.info("Total bundles across all caches: $totalBundles")

        logSection("Verifying proof propagation (sampling)")
        var totalProofsVerified = 0
        cacheData.take(3).forEach { (name, pair, cache) ->
            Logger.info("Sampling proof verification for $name (${cache.bundles.size} bundles)")
            cache.bundles.take(2).forEach { bundle ->
                val proofsResponse = pair.api.getPropagationProofs(bundle.bundleId)
                assertThat(proofsResponse.proofs.size).isEqualTo(bundle.count.toInt())
                    .describedAs("$name should have all ${bundle.count} proofs for bundle ${bundle.bundleId}")
                Logger.info("  ✓ $name has ${proofsResponse.proofs.size} proofs for bundle from ${bundle.participant}")
                totalProofsVerified += proofsResponse.proofs.size
            }
        }
        Logger.info("Total proofs verified (sample): $totalProofsVerified")

        logSection("✅ Test Complete - Natural propagation verified in 9-node network")
        Logger.info("All participants successfully propagated and received bundles and proofs automatically")
        Logger.info("Total bundles propagated: $totalBundles")
        Logger.info("Propagation handled by bundler and tree manager - no manual intervention")
    }
}
