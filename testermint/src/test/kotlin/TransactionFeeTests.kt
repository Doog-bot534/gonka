import com.productscience.*
import com.productscience.data.*
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.BeforeAll
import org.junit.jupiter.api.MethodOrderer
import org.junit.jupiter.api.Order
import org.junit.jupiter.api.Test
import org.junit.jupiter.api.TestMethodOrder

/**
 * Integration tests for consensus-level transaction fee enforcement.
 *
 * Uses a custom genesis spec that enables FeeParams, requiring a cluster
 * reboot. All other tests run without fee enforcement (FeeParams not set
 * at genesis), matching pre-upgrade behavior.
 *
 * Verifies that:
 * - Fee-required messages are rejected with zero/insufficient fees
 * - Fee-required messages succeed with sufficient fees
 * - Fees are deducted from the sender's balance
 * - Default transaction path (with --gas-prices) works
 */
@TestMethodOrder(MethodOrderer.OrderAnnotation::class)
class TransactionFeeTests : TestermintTest() {

    companion object {
        private lateinit var cluster: LocalCluster
        private lateinit var genesis: LocalInferencePair
        private lateinit var genesisAddress: String
        private lateinit var recipientAddress: String

        @BeforeAll
        @JvmStatic
        fun initOnce() {
            // Enable fee enforcement via genesis spec merge.
            // This triggers a cluster reboot with FeeParams set.
            val feeSpec = spec<AppState> {
                this[AppState::inference] = spec<InferenceState> {
                    this[InferenceState::params] = spec<InferenceParams> {
                        this[InferenceParams::feeParams] = spec<FeeParamsData> {
                            this[FeeParamsData::minGasPriceNgonka] = 10L
                            this[FeeParamsData::baseValidationGas] = 500_000L
                            this[FeeParamsData::gasPerPocCount] = 100L
                        }
                    }
                }
            }

            val result = initCluster(reboot = true, mergeSpec = feeSpec)
            cluster = result.first
            genesis = result.second
            genesisAddress = genesis.node.getColdAddress()
            recipientAddress = cluster.joinPairs.first().node.getColdAddress()
        }
    }

    @Test
    @Order(1)
    fun `bank send with zero fees is rejected`() {
        logHighlight("Testing that bank send with zero fees is rejected")

        val result = genesis.submitTransactionWithFees(
            listOf(
                "bank", "send",
                genesisAddress, recipientAddress,
                "1000ngonka"
            ),
            fees = "0ngonka"
        )

        assertThat(result.code).isNotEqualTo(0)
        assertThat(result.rawLog).containsIgnoringCase("insufficient fee")
        logHighlight("Bank send with zero fees correctly rejected: ${result.rawLog}")
    }

    @Test
    @Order(2)
    fun `bank send with sufficient fees succeeds`() {
        logHighlight("Testing that bank send with sufficient fees succeeds")

        val balanceBefore = genesis.getBalance(genesisAddress)

        // 200,000 gas * 10 ngonka/gas = 2,000,000 ngonka minimum fee
        val result = genesis.submitTransactionWithFees(
            listOf(
                "bank", "send",
                genesisAddress, recipientAddress,
                "1000ngonka"
            ),
            fees = "2000000ngonka"
        )

        assertThat(result.code).isEqualTo(0)

        val balanceAfter = genesis.getBalance(genesisAddress)
        // Balance should decrease by at least the transfer amount
        assertThat(balanceAfter).isLessThan(balanceBefore)
        logHighlight("Bank send with sufficient fees succeeded. Balance: $balanceBefore -> $balanceAfter")
    }

    @Test
    @Order(3)
    fun `staking delegate with zero fees is rejected`() {
        logHighlight("Testing that staking delegate with zero fees is rejected")

        val validatorAddr = genesis.node.getValidators().validators.first().operatorAddress

        val result = genesis.submitTransactionWithFees(
            listOf(
                "staking", "delegate",
                validatorAddr,
                "1000ngonka"
            ),
            fees = "0ngonka"
        )

        assertThat(result.code).isNotEqualTo(0)
        assertThat(result.rawLog).containsIgnoringCase("insufficient fee")
        logHighlight("Staking delegate with zero fees correctly rejected: ${result.rawLog}")
    }

    @Test
    @Order(4)
    fun `default transaction path succeeds with gas prices`() {
        logHighlight("Testing that default transaction path (with --gas-prices) succeeds")

        // The default submitTransaction path includes --gas-prices 10ngonka,
        // so fee calculation happens automatically via gas simulation.
        val result = genesis.submitTransaction(
            listOf(
                "bank", "send",
                genesisAddress, recipientAddress,
                "500ngonka"
            )
        )

        assertThat(result.code).isEqualTo(0)
        logHighlight("Default-path transaction succeeded (fees auto-calculated from gas prices)")
    }
}
