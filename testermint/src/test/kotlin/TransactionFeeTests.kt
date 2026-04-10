import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.BeforeAll
import org.junit.jupiter.api.MethodOrderer
import org.junit.jupiter.api.Order
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.TestMethodOrder

/**
 * Integration tests for transaction fee enforcement lifecycle.
 *
 * Tests the full flow:
 * 1. Verify no fee enforcement at genesis (FeeParams nil)
 * 2. Verify inference works before fees
 * 3. Enable fee enforcement via governance proposal (simulates v0.2.12 upgrade)
 * 4. Verify fee-required messages are rejected without sufficient fees (via CLI)
 * 5. Verify fee-required messages succeed with sufficient fees (via CLI)
 *
 * Note: Post-enablement inference/PoC tests are not included because the DAPI
 * containers cannot be reconfigured with gas prices mid-test. Fee-exempt bypass
 * for inference and PoC messages is covered by unit tests in ante_fee_test.go.
 * MsgClaimRewards (fee-required) will fail from the DAPI after fees are enabled
 * since the DAPI has min_gas_price_ngonka=0 — this is expected and matches the
 * production rollout where DAPI config is updated alongside the upgrade.
 */
@TestMethodOrder(MethodOrderer.OrderAnnotation::class)
class TransactionFeeTests : TestermintTest() {

    companion object {
        private lateinit var cluster: LocalCluster
        private lateinit var genesis: LocalInferencePair
        private lateinit var genesisAddress: String

        @BeforeAll
        @JvmStatic
        fun initOnce() {
            val result = initCluster()
            cluster = result.first
            genesis = result.second
            genesisAddress = genesis.node.getColdAddress()
        }
    }

    // ========== PRE-UPGRADE ==========

    @Test
    @Order(1)
    fun `fee params are nil at genesis`() {
        logHighlight("Verifying FeeParams are not set at genesis")

        val params = genesis.getParams()
        assertThat(params.feeParams).isNull()
        logHighlight("FeeParams correctly nil at genesis")
    }

    @Test
    @Order(2)
    fun `inference succeeds before fee enablement`() {
        logHighlight("Testing that inference works before fee enforcement is enabled")

        genesis.waitForNextInferenceWindow()
        val response = genesis.makeInferenceRequest(inferenceRequest)

        assertThat(response.choices).isNotEmpty
        logHighlight("Inference succeeded pre-fees: model=${response.model}")
    }

    // ========== ENABLE FEES ==========

    @Test
    @Order(3)
    fun `enable fee enforcement via governance proposal`() {
        logHighlight("Enabling fee enforcement via governance (simulates v0.2.12 upgrade)")

        val params = genesis.getParams()
        val paramsWithFees = params.copy(
            feeParams = FeeParamsData(
                minGasPriceNgonka = 10,
                baseValidationGas = 500_000,
                gasPerPocCount = 100,
            )
        )

        genesis.runProposal(cluster, UpdateParams(params = paramsWithFees))
        genesis.node.waitForNextBlock(2)
        logHighlight("Fee enforcement proposal passed")
    }

    // ========== POST-UPGRADE: CLI rejection tests ==========
    // These use the CLI directly (not the DAPI) so they work even though
    // the DAPI containers don't have gas prices configured.

    @Test
    @Order(4)
    fun `zero-fee collateral deposit rejected`() {
        logHighlight("Testing zero-fee collateral deposit is rejected")

        val result = genesis.submitTransactionWithFees(
            listOf("collateral", "deposit-collateral", "1000000ngonka"),
            fees = "0ngonka"
        )

        assertThat(result.code).isNotEqualTo(0)
        assertThat(result.rawLog).containsIgnoringCase("insufficient fee")
        logHighlight("Zero-fee collateral deposit rejected: code=${result.code}")
    }

    @Test
    @Order(5)
    fun `insufficient fee rejected`() {
        logHighlight("Testing insufficient fee is rejected")

        // At 10 ngonka/gas and 200k gas, minimum fee is 2,000,000 ngonka.
        val result = genesis.submitTransactionWithFees(
            listOf("collateral", "deposit-collateral", "1000000ngonka"),
            fees = "1ngonka"
        )

        assertThat(result.code).isNotEqualTo(0)
        assertThat(result.rawLog).containsIgnoringCase("insufficient fee")
        logHighlight("Insufficient fee rejected: code=${result.code}")
    }

    @Test
    @Order(6)
    fun `sufficient fee succeeds and deducts balance`() {
        logHighlight("Testing sufficient-fee collateral deposit succeeds")

        val balanceBefore = genesis.getBalance(genesisAddress)

        val result = genesis.submitTransactionWithFees(
            listOf("collateral", "deposit-collateral", "1000000ngonka"),
            fees = "5000000ngonka"
        )

        assertThat(result.code).isEqualTo(0)

        val balanceAfter = genesis.getBalance(genesisAddress)
        val deducted = balanceBefore - balanceAfter
        assertThat(deducted).isGreaterThanOrEqualTo(1_000_000 + 5_000_000)
        logHighlight("Balance deducted: $deducted ngonka (collateral=1M + fee=5M)")
    }

    // ========== POST-UPGRADE: DAPI continues to function ==========
    // The DAPI's warm key signs txs but the cold key pays fees via the
    // feegrant allowance set up by grant-ml-ops-permissions. Inference and
    // PoC duty messages are fee-exempt via the bypass decorator.

    @Test
    @Order(7)
    fun `inference succeeds after fee enablement via feegrant`() {
        logHighlight("Testing that DAPI inference pipeline works post-upgrade")
        logHighlight("MsgStartInference and MsgFinishInference are fee-exempt; warm key uses feegrant for fee-required msgs")

        // Wait for the cluster to stabilize after governance params change
        genesis.waitForNextEpoch()
        genesis.waitForNextInferenceWindow()

        val response = genesis.makeInferenceRequest(inferenceRequest)
        assertThat(response.choices).isNotEmpty
        logHighlight("Post-upgrade inference succeeded: model=${response.model}")
    }

    @Test
    @Order(8)
    fun `PoC epoch completes after fee enablement`() {
        logHighlight("Testing that a full PoC epoch completes post-upgrade")
        logHighlight("PoC commit messages are fee-required (count-linear); paid via feegrant")

        val epochBefore = genesis.getEpochData().latestEpoch.index
        genesis.waitForNextEpoch()
        val epochAfter = genesis.getEpochData().latestEpoch.index

        assertThat(epochAfter).isGreaterThan(epochBefore)
        logHighlight("Epoch advanced from $epochBefore to $epochAfter post-upgrade")
    }
}
