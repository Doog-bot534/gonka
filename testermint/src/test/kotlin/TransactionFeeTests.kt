import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.BeforeAll
import org.junit.jupiter.api.MethodOrderer
import org.junit.jupiter.api.Order
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.TestMethodOrder

/**
 * Integration tests for the transaction fee enforcement lifecycle.
 *
 * Tests the complete pre-upgrade → upgrade → post-upgrade flow:
 *
 * Pre-upgrade (no fees):
 *   - FeeParams nil at genesis
 *   - Inference works without fees
 *
 * Upgrade (enable fees via governance):
 *   - Submit MsgUpdateParams with FeeParams via governance proposal
 *   - Verify FeeParams are set on-chain
 *
 * Post-upgrade (fees enforced):
 *   - Zero-fee transactions rejected (staking, governance)
 *   - Insufficient-fee transactions rejected
 *   - Sufficient-fee transactions succeed and deduct balance
 *   - Fee-exempt inference pipeline still works
 *   - PoC epoch cycle completes (fee-exempt PoC + validation messages)
 *   - Collateral operations work with fees
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
        logHighlight("Testing inference works before fee enforcement")

        genesis.waitForNextInferenceWindow()
        val response = genesis.makeInferenceRequest(inferenceRequest)

        assertThat(response.choices).isNotEmpty
        logHighlight("Pre-upgrade inference succeeded: model=${response.model}")
    }

    // ========== UPGRADE (enable fees) ==========

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
        genesis.markNeedsReboot()

        val newParams = genesis.getParams()
        assertThat(newParams.feeParams).isNotNull
        assertThat(newParams.feeParams!!.minGasPriceNgonka).isEqualTo(10L)
        assertThat(newParams.feeParams!!.baseValidationGas).isEqualTo(500_000L)
        assertThat(newParams.feeParams!!.gasPerPocCount).isEqualTo(100L)
        logHighlight("Fee enforcement enabled successfully")
    }

    // ========== POST-UPGRADE: rejection tests ==========

    @Test
    @Order(4)
    fun `zero-fee staking delegate rejected`() {
        logHighlight("Testing zero-fee staking delegate is rejected")

        val validatorAddr = genesis.node.getValidators().validators.first().operatorAddress

        val result = genesis.submitTransactionWithFees(
            listOf("staking", "delegate", validatorAddr, "1000ngonka"),
            fees = "0ngonka"
        )

        assertThat(result.code).isNotEqualTo(0)
        assertThat(result.rawLog).containsIgnoringCase("insufficient fee")
        logHighlight("Zero-fee staking delegate rejected: code=${result.code}")
    }

    @Test
    @Order(5)
    fun `insufficient fee rejected`() {
        logHighlight("Testing insufficient fee is rejected")

        val validatorAddr = genesis.node.getValidators().validators.first().operatorAddress

        // At 10 ngonka/gas and 200k gas, minimum fee is 2,000,000 ngonka.
        // Send only 1 ngonka.
        val result = genesis.submitTransactionWithFees(
            listOf("staking", "delegate", validatorAddr, "1000ngonka"),
            fees = "1ngonka"
        )

        assertThat(result.code).isNotEqualTo(0)
        assertThat(result.rawLog).containsIgnoringCase("insufficient fee")
        logHighlight("Insufficient fee rejected: code=${result.code}")
    }

    // ========== POST-UPGRADE: acceptance tests ==========

    @Test
    @Order(6)
    fun `sufficient fee succeeds and deducts balance`() {
        logHighlight("Testing sufficient-fee collateral deposit succeeds")

        val balanceBefore = genesis.getBalance(genesisAddress)

        // Collateral deposit with explicit fee
        val result = genesis.submitTransactionWithFees(
            listOf("collateral", "deposit-collateral", "1000000ngonka"),
            fees = "5000000ngonka"
        )

        assertThat(result.code).isEqualTo(0)

        val balanceAfter = genesis.getBalance(genesisAddress)
        val deducted = balanceBefore - balanceAfter
        // Should deduct collateral (1M) + fee (5M)
        assertThat(deducted).isGreaterThanOrEqualTo(1_000_000 + 5_000_000)
        logHighlight("Collateral deposit succeeded, balance deducted: $deducted ngonka")
    }

    // ========== POST-UPGRADE: fee-exempt bypass tests ==========

    @Test
    @Order(7)
    fun `inference succeeds after fee enablement`() {
        logHighlight("Testing inference pipeline works post-upgrade (MsgStartInference + MsgFinishInference are fee-exempt)")

        genesis.waitForNextInferenceWindow()
        val response = genesis.makeInferenceRequest(inferenceRequest)

        assertThat(response.choices).isNotEmpty
        logHighlight("Post-upgrade inference succeeded: model=${response.model}")
    }

    @Test
    @Order(8)
    fun `PoC epoch completes after fee enablement`() {
        logHighlight("Testing that PoC epoch cycle completes post-upgrade (PoC messages are fee-exempt)")

        // Wait for a full epoch to pass — PoC submissions, validations,
        // weight distributions, and BLS messages are all fee-exempt.
        genesis.waitForNextEpoch()

        // Verify the node still has weight (participated successfully in the epoch)
        val stats = genesis.node.getParticipantCurrentStats()
        val weight = stats.participantCurrentStats
            ?.find { it.participantId == genesisAddress }
            ?.weight ?: 0
        assertThat(weight).isGreaterThan(0)
        logHighlight("Post-upgrade PoC epoch completed, genesis weight=$weight")
    }

    @Test
    @Order(9)
    fun `multiple inferences succeed after fee enablement`() {
        logHighlight("Testing multiple sequential inferences post-upgrade")

        genesis.waitForNextInferenceWindow()

        repeat(3) { i ->
            val response = genesis.makeInferenceRequest(inferenceRequest)
            assertThat(response.choices).isNotEmpty
            logHighlight("Post-upgrade inference ${i + 1}/3 succeeded")
        }
    }
}
