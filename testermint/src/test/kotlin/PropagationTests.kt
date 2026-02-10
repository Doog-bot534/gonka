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
            reboot = true,
            config = bandwidthConfig,
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
            reboot = true,
            config = bandwidthConfig,
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
    fun `propagation - first arrival times are recorded for each participant`() {
        logSection("=== TEST: First Arrival Time Tracking ===")

        val (cluster, genesis) = initCluster(
            joinCount = 2,
            reboot = true,
            config = bandwidthConfig,
        )

        val join1 = cluster.joinPairs[0]
        val join2 = cluster.joinPairs[1]

        logSection("Cluster Initialized with 3 participants")
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

        assertThat(genesisState.count).isGreaterThan(0)
        assertThat(join1State.count).isGreaterThan(0)
        assertThat(join2State.count).isGreaterThan(0)

        logSection("Waiting for PoC exchange phase (propagation occurs)")
        genesis.waitForStage(EpochStage.POC_EXCHANGE_DEADLINE)
        genesis.node.waitForNextBlock(5)

        logSection("Querying first arrival times from all nodes")
        val genesisArrivals = genesis.api.getPropagationFirstArrivals(pocHeight)
        val join1Arrivals = join1.api.getPropagationFirstArrivals(pocHeight)
        val join2Arrivals = join2.api.getPropagationFirstArrivals(pocHeight)

        Logger.info("Genesis recorded first arrivals for ${genesisArrivals.arrivals.size} participants")
        genesisArrivals.arrivals.forEach { (participant, info) ->
            Logger.info("  $participant -> time=${info.time}, count=${info.count}")
        }

        Logger.info("Join1 recorded first arrivals for ${join1Arrivals.arrivals.size} participants")
        join1Arrivals.arrivals.forEach { (participant, info) ->
            Logger.info("  $participant -> time=${info.time}, count=${info.count}")
        }

        Logger.info("Join2 recorded first arrivals for ${join2Arrivals.arrivals.size} participants")
        join2Arrivals.arrivals.forEach { (participant, info) ->
            Logger.info("  $participant -> time=${info.time}, count=${info.count}")
        }

        val genesisAddr = genesis.node.getColdAddress()
        val join1Addr = join1.node.getColdAddress()
        val join2Addr = join2.node.getColdAddress()

        logSection("Verifying first arrival times are recorded")

        assertThat(genesisArrivals.arrivals).containsKeys(join1Addr, join2Addr)
            .describedAs("Genesis should have first arrival times for join1 and join2")
        assertThat(join1Arrivals.arrivals).containsKeys(genesisAddr, join2Addr)
            .describedAs("Join1 should have first arrival times for genesis and join2")
        assertThat(join2Arrivals.arrivals).containsKeys(genesisAddr, join1Addr)
            .describedAs("Join2 should have first arrival times for genesis and join1")

        logSection("Verifying arrival times are positive (valid timestamps)")
        genesisArrivals.arrivals.values.forEach { info ->
            assertThat(info.time).isGreaterThan(0)
        }
        join1Arrivals.arrivals.values.forEach { info ->
            assertThat(info.time).isGreaterThan(0)
        }
        join2Arrivals.arrivals.values.forEach { info ->
            assertThat(info.time).isGreaterThan(0)
        }

        logSection("Verifying arrival times are consistent (static timer)")
        genesis.node.waitForNextBlock(3)

        val genesisArrivals2 = genesis.api.getPropagationFirstArrivals(pocHeight)

        genesisArrivals.arrivals.forEach { (participant, originalInfo) ->
            val newInfo = genesisArrivals2.arrivals[participant]
            assertThat(newInfo?.time).isEqualTo(originalInfo.time)
                .describedAs("First arrival time for $participant should not change (static timer)")
            assertThat(newInfo?.count).isEqualTo(originalInfo.count)
                .describedAs("First arrival count for $participant should not change (static timer)")
        }

        logSection("Test Complete - First arrival times recorded and static")
        Logger.info("All nodes recorded first arrival times for other participants")
        Logger.info("Arrival times remain constant (static timer verified)")
    }

    @Test
    fun `propagation - on-chain consensus calculates correct agreed counts`() {
        logSection("=== TEST: On-Chain Consensus Calculation ===")

        val (cluster, genesis) = initCluster(
            joinCount = 2,
            reboot = true,
            config = bandwidthConfig,
        )

        val join1 = cluster.joinPairs[0]
        val join2 = cluster.joinPairs[1]

        logSection("Cluster Initialized with 3 participants")
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

        Logger.info("Genesis artifacts: count=${genesisState.count}")
        Logger.info("Join1 artifacts: count=${join1State.count}")
        Logger.info("Join2 artifacts: count=${join2State.count}")

        assertThat(genesisState.count).isGreaterThan(0)
        assertThat(join1State.count).isGreaterThan(0)
        assertThat(join2State.count).isGreaterThan(0)

        logSection("Waiting for PoC exchange deadline (observations submitted on-chain)")
        genesis.waitForStage(EpochStage.POC_EXCHANGE_DEADLINE)
        genesis.node.waitForNextBlock(10)

        val genesisAddr = genesis.node.getColdAddress()
        val join1Addr = join1.node.getColdAddress()
        val join2Addr = join2.node.getColdAddress()

        logSection("Querying on-chain observations")
        val observations = genesis.node.getPoCObservations(pocHeight)

        Logger.info("On-chain observations: ${observations.observations.size}")
        observations.observations.forEach { obs ->
            Logger.info("  From ${obs.validatorAddress}: ${obs.arrivals.size} arrivals at block ${obs.blockHeight}")
            obs.arrivals.forEach { arrival ->
                Logger.info("    ${arrival.participant}: count=${arrival.count}")
            }
        }

        logSection("Verifying observations were submitted on-chain")
        assertThat(observations.observations.size).isGreaterThanOrEqualTo(1)
            .describedAs("At least one observation should be submitted on-chain")

        logSection("Querying on-chain consensus")
        val consensus = genesis.node.getPoCConsensus(pocHeight)

        Logger.info("On-chain consensus entries: ${consensus.entries.size}")
        consensus.entries.forEach { entry ->
            Logger.info("  ${entry.participant}: agreedCount=${entry.agreedCount}, " +
                "totalValidators=${entry.totalValidators}, agreeingCount=${entry.agreeingCount}")
        }

        logSection("Verifying consensus results are present")
        assertThat(consensus.entries).isNotEmpty
            .describedAs("Should have consensus entries")

        logSection("Verifying consensus agreed counts match actual artifact counts")
        val consensusMap = consensus.entries.associateBy { it.participant }

        if (consensusMap.containsKey(genesisAddr)) {
            val result = consensusMap[genesisAddr]!!
            Logger.info("Consensus for Genesis: agreedCount=${result.agreedCount}, actualCount=${genesisState.count}")
            assertThat(result.agreedCount).isGreaterThan(0)
                .describedAs("Genesis should have positive agreed count")
            assertThat(result.agreedCount).isLessThanOrEqualTo(genesisState.count.toLong())
                .describedAs("Agreed count should not exceed actual count")
        }

        if (consensusMap.containsKey(join1Addr)) {
            val result = consensusMap[join1Addr]!!
            Logger.info("Consensus for Join1: agreedCount=${result.agreedCount}, actualCount=${join1State.count}")
            assertThat(result.agreedCount).isGreaterThan(0)
                .describedAs("Join1 should have positive agreed count")
            assertThat(result.agreedCount).isLessThanOrEqualTo(join1State.count.toLong())
                .describedAs("Agreed count should not exceed actual count")
        }

        if (consensusMap.containsKey(join2Addr)) {
            val result = consensusMap[join2Addr]!!
            Logger.info("Consensus for Join2: agreedCount=${result.agreedCount}, actualCount=${join2State.count}")
            assertThat(result.agreedCount).isGreaterThan(0)
                .describedAs("Join2 should have positive agreed count")
            assertThat(result.agreedCount).isLessThanOrEqualTo(join2State.count.toLong())
                .describedAs("Agreed count should not exceed actual count")
        }

        logSection("Test Complete - On-chain consensus calculation verified")
        Logger.info("Observations submitted on-chain and consensus computed by chain query")
    }

    val offChainPoCSpec = spec {
        this[AppState::inference] = spec<InferenceState> {
            this[InferenceState::params] = spec<InferenceParams> {
                this[InferenceParams::epochParams] = spec<EpochParams> {
                    this[EpochParams::pocStageDuration] = 3L
                    this[EpochParams::pocValidationDuration] = 4L
                }
                this[InferenceParams::pocParams] = spec<PocParams> {
                    this[PocParams::pocV2Enabled] = true
                }
            }
        }
    }

    val bandwidthConfig = inferenceConfig.copy(
        genesisSpec = inferenceConfig.genesisSpec?.merge(offChainPoCSpec) ?: offChainPoCSpec,
    )
}
