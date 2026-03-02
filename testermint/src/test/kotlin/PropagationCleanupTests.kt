import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.Timeout
import org.tinylog.kotlin.Logger
import java.util.concurrent.TimeUnit

/**
 * Tests for propagation proof cleanup logic.
 * Verifies that old propagation data (bundles and proofs) are cleaned up
 * when entering epoch N, data from epoch N-2 should be deleted.
 */
@Timeout(value = 20, unit = TimeUnit.MINUTES)
class PropagationCleanupTests : TestermintTest() {

    /**
     * Test that propagation cache is cleaned up after 3 epochs.
     * When entering epoch 3, data from epoch 1 should be deleted.
     */
    @Test
    fun `propagation cleanup - old epoch data is deleted after 2 epochs`() {
        logSection("=== TEST: Propagation Cleanup - Old Epoch Data Deletion ===")

        val (cluster, genesis) = initCluster(
            joinCount = 2,
            reboot = true,
            config = cleanupTestConfig,
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

        // === EPOCH 1: Generate and propagate data ===
        logSection("EPOCH 1: Waiting for PoC generation phase")
        genesis.waitForStage(EpochStage.START_OF_POC)
        genesis.node.waitForNextBlock(3)

        val epoch1Data = genesis.getEpochData()
        val epoch1PocHeight = epoch1Data.latestEpoch.pocStartBlockHeight
        val epoch1Index = epoch1Data.latestEpoch.index
        Logger.info("Epoch 1: index=$epoch1Index, pocHeight=$epoch1PocHeight")

        // Wait for propagation to complete
        genesis.waitForStage(EpochStage.POC_EXCHANGE_DEADLINE)
        genesis.node.waitForNextBlock(3)

        // Verify epoch 1 data exists in cache
        val epoch1Cache = genesis.api.getPropagationCache(epoch1PocHeight)
        Logger.info("Epoch 1 cache: ${epoch1Cache.count} bundles")
        assertThat(epoch1Cache.count).isGreaterThan(0)
            .describedAs("Epoch 1 should have propagation data")

        // === EPOCH 2: Generate and propagate data ===
        logSection("EPOCH 2: Waiting for next PoC generation phase")
        genesis.waitForStage(EpochStage.START_OF_POC, offset = 1)
        genesis.node.waitForNextBlock(3)

        val epoch2Data = genesis.getEpochData()
        val epoch2PocHeight = epoch2Data.latestEpoch.pocStartBlockHeight
        val epoch2Index = epoch2Data.latestEpoch.index
        Logger.info("Epoch 2: index=$epoch2Index, pocHeight=$epoch2PocHeight")

        assertThat(epoch2Index).isEqualTo(epoch1Index + 1)
            .describedAs("Should be in epoch 2")

        // Wait for propagation to complete
        genesis.waitForStage(EpochStage.POC_EXCHANGE_DEADLINE)
        genesis.node.waitForNextBlock(3)

        // Verify epoch 2 data exists
        val epoch2Cache = genesis.api.getPropagationCache(epoch2PocHeight)
        Logger.info("Epoch 2 cache: ${epoch2Cache.count} bundles")
        assertThat(epoch2Cache.count).isGreaterThan(0)
            .describedAs("Epoch 2 should have propagation data")

        // Verify epoch 1 data STILL exists (not cleaned yet)
        val epoch1CacheStillExists = genesis.api.getPropagationCache(epoch1PocHeight)
        Logger.info("Epoch 1 cache (during epoch 2): ${epoch1CacheStillExists.count} bundles")
        assertThat(epoch1CacheStillExists.count).isGreaterThan(0)
            .describedAs("Epoch 1 data should still exist during epoch 2")

        // === EPOCH 3: Cleanup should trigger ===
        logSection("EPOCH 3: Waiting for PoC start (cleanup should trigger)")
        genesis.waitForStage(EpochStage.START_OF_POC, offset = 1)
        genesis.node.waitForNextBlock(5) // Give cleanup goroutine time to run

        val epoch3Data = genesis.getEpochData()
        val epoch3PocHeight = epoch3Data.latestEpoch.pocStartBlockHeight
        val epoch3Index = epoch3Data.latestEpoch.index
        Logger.info("Epoch 3: index=$epoch3Index, pocHeight=$epoch3PocHeight")

        assertThat(epoch3Index).isEqualTo(epoch1Index + 2)
            .describedAs("Should be in epoch 3")

        // Verify epoch 1 data is NOW DELETED
        logSection("Verifying epoch 1 data was cleaned up")
        val epoch1CacheAfterCleanup = genesis.api.getPropagationCache(epoch1PocHeight)
        Logger.info("Epoch 1 cache (after cleanup): ${epoch1CacheAfterCleanup.count} bundles")
        assertThat(epoch1CacheAfterCleanup.count).isEqualTo(0)
            .describedAs("Epoch 1 data should be deleted when entering epoch 3")

        // Verify epoch 2 data STILL exists (N-1 is kept)
        val epoch2CacheStillExists = genesis.api.getPropagationCache(epoch2PocHeight)
        Logger.info("Epoch 2 cache (after epoch 1 cleanup): ${epoch2CacheStillExists.count} bundles")
        assertThat(epoch2CacheStillExists.count).isGreaterThan(0)
            .describedAs("Epoch 2 data should still exist (only N-2 is deleted)")

        // Verify cleanup happened on all nodes
        logSection("Verifying cleanup on all nodes")
        val join1Epoch1Cache = join1.api.getPropagationCache(epoch1PocHeight)
        val join2Epoch1Cache = join2.api.getPropagationCache(epoch1PocHeight)

        Logger.info("Join1 epoch 1 cache: ${join1Epoch1Cache.count} bundles")
        Logger.info("Join2 epoch 1 cache: ${join2Epoch1Cache.count} bundles")

        assertThat(join1Epoch1Cache.count).isEqualTo(0)
            .describedAs("Join1 should have cleaned up epoch 1 data")
        assertThat(join2Epoch1Cache.count).isEqualTo(0)
            .describedAs("Join2 should have cleaned up epoch 1 data")

        logSection("TEST PASSED: Propagation cleanup works correctly")
        Logger.info("Epoch 1 data deleted when entering epoch 3")
        Logger.info("Epoch 2 data preserved (N-1 kept for validation recovery)")
    }

    // Short epoch length to make test run faster (3 epochs needed)
    // EpochLength = 25 means ~75 blocks total for 3 epochs
    val cleanupTestSpec = spec {
        this[AppState::inference] = spec<InferenceState> {
            this[InferenceState::params] = spec<InferenceParams> {
                this[InferenceParams::epochParams] = spec<EpochParams> {
                    this[EpochParams::epochLength] = 15L
                    this[EpochParams::pocStageDuration] = 3L
                    this[EpochParams::pocExchangeDuration] = 2L
                    this[EpochParams::pocValidationDelay] = 5L
                    this[EpochParams::pocValidationDuration] = 3L
                }
                this[InferenceParams::pocParams] = spec<PocParams> {
                    this[PocParams::pocV2Enabled] = true
                }
            }
        }
    }

    val cleanupTestConfig = inferenceConfig.copy(
        genesisSpec = inferenceConfig.genesisSpec?.merge(cleanupTestSpec) ?: cleanupTestSpec,
    )
}
