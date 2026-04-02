import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.BeforeAll
import org.junit.jupiter.api.MethodOrderer
import org.junit.jupiter.api.Order
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.TestMethodOrder

/**
 * Integration tests verifying transaction fee infrastructure.
 *
 * These tests run on a standard cluster WITHOUT fee enforcement (FeeParams
 * not set at genesis). They verify that the DAPI and CLI transaction paths
 * work correctly with the fee infrastructure in place.
 *
 * Consensus-level fee enforcement logic (rejection of zero-fee txs, bypass
 * for duty messages, count-linear PoC fees) is covered by unit tests in
 * ante_fee_test.go.
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

    @Test
    @Order(1)
    fun `fee params are nil at genesis`() {
        logHighlight("Verifying FeeParams are not set at genesis")

        val params = genesis.getParams()
        // FeeParams should be null/absent in genesis (enabled via upgrade handler)
        assertThat(params.feeParams).isNull()
        logHighlight("FeeParams correctly nil at genesis — fees enabled via v0.2.12 upgrade")
    }

    @Test
    @Order(2)
    fun `staking delegate succeeds without fee enforcement`() {
        logHighlight("Testing that staking works without fee enforcement")

        val validatorAddr = genesis.node.getValidators().validators.first().operatorAddress

        // Zero-fee staking delegate should succeed when fee enforcement is not active
        val result = genesis.submitTransactionWithFees(
            listOf(
                "staking", "delegate",
                validatorAddr,
                "1000ngonka"
            ),
            fees = "0ngonka"
        )

        assertThat(result.code).isEqualTo(0)
        logHighlight("Staking delegate with zero fees succeeded (no fee enforcement active)")
    }

    @Test
    @Order(3)
    fun `inference request succeeds without fee enforcement`() {
        logHighlight("Testing that inference pipeline works with fee infrastructure in place")

        genesis.waitForNextInferenceWindow()
        val response = genesis.makeInferenceRequest(inferenceRequest)

        assertThat(response.choices).isNotEmpty
        logHighlight("Inference succeeded: model=${response.model}")
    }
}
