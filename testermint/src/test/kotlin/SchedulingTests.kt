import com.productscience.ApplicationCLI
import com.productscience.EpochStage
import com.productscience.LocalInferencePair
import com.productscience.data.*
import com.productscience.defaultModel
import com.productscience.initCluster
import com.productscience.validNode
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Timeout
import java.util.concurrent.TimeUnit
import kotlin.test.Test

@Timeout(value = 15, unit = TimeUnit.MINUTES)
class SchedulingTests : TestermintTest() {
    @Test
    fun basicSchedulingTest() {
        val (cluster, genesis) = initCluster(reboot = true, resetMlNodes = false)
        genesis.addNodes(1)
        genesis.waitForNextEpoch()
        val genesisParticipantKey = genesis.node.getValidatorInfo()

        // Wait for all participants to join and validators to be applied
        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)
        
        checkParticipantWeights(genesis.node, genesisParticipantKey) // Should have all participants by now

        val allocatedNode = genesis.api.getNodes().let { nodes ->
            assertThat(nodes).hasSize(2)
            nodes.forEach { node ->
                node.state.epochMlNodes?.forEach { (_, value) ->
                    assertThat(value.pocWeight).isEqualTo(10)
                    assertThat(value.timeslotAllocation).hasSize(2)
                }
            }
            nodes.firstNotNullOf { node ->
                val isAllocatedForInference = node.state.epochMlNodes
                    ?.firstNotNullOf { (_, x) -> x.timeslotAllocation.getOrNull(1) == true  }
                    ?: false
                node.takeIf { isAllocatedForInference }
            }
        }

        assertThat(allocatedNode).isNotNull

        genesis.waitForStage(EpochStage.START_OF_POC)

        genesis.api.getNodes().let { nodes ->
            assertThat(nodes).hasSize(2)
            nodes.forEach { node ->
                node.state.epochMlNodes?.forEach { (_, value) ->
                    assertThat(value.pocWeight).isEqualTo(10)
                    assertThat(value.timeslotAllocation).hasSize(2)
                }
            }
            nodes.forEach { node ->
                if (node.node.id == allocatedNode.node.id) {
                    assertThat(node.state.currentStatus).isEqualTo("INFERENCE")
                    assertThat(node.state.intendedStatus).isEqualTo("INFERENCE")
                } else {
                    assertThat(node.state.currentStatus).isEqualTo("POC")
                    assertThat(node.state.intendedStatus).isEqualTo("POC")
                }
            }
        }

        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)

        checkParticipantWeights(genesis.node, genesisParticipantKey)

        val allocatedNode2 = genesis.api.getNodes().let { nodes ->
            assertThat(nodes).hasSize(2)

            nodes.forEach { node ->
                node.state.epochMlNodes?.forEach { (key, value) ->
                    assertThat(value.pocWeight).isEqualTo(10)
                    assertThat(value.timeslotAllocation).hasSize(2)
                }
            }

            nodes.forEach { node ->
                assertThat(node.state.currentStatus).isEqualTo("INFERENCE")
                assertThat(node.state.intendedStatus).isEqualTo("INFERENCE")
            }

            nodes.firstNotNullOf { node ->
                val isAllocatedForInference = node.state.epochMlNodes
                    ?.firstNotNullOf { (_, x) -> x.timeslotAllocation.getOrNull(1) == true  }
                    ?: false
                node.takeIf { isAllocatedForInference }
            }
        }

        assertThat(allocatedNode2).isNotNull
    }

    @Test
    fun `shared node ids do not filter other participant PoC batches`() {
        val pocSlotSpec = spec {
            this[AppState::inference] = spec<InferenceState> {
                this[InferenceState::genesisOnlyParams] = spec<GenesisOnlyParams> {
                    this[GenesisOnlyParams::maxIndividualPowerPercentage] = Decimal.fromDouble(0.0)
                }
                this[InferenceState::params] = spec<InferenceParams> {
                    this[InferenceParams::epochParams] = spec<EpochParams> {
                        this[EpochParams::pocSlotAllocation] = Decimal.fromDouble(0.25)
                    }
                }
            }
        }

        val (cluster, genesis) = initCluster(
            joinCount = 1,
            mergeSpec = pocSlotSpec,
            reboot = true,
            resetMlNodes = false
        )
        val join = cluster.joinPairs.first()
        val nodeIds = listOf("node-1", "node-2", "node-3", "node-4")

        genesis.api.setNodesTo(buildNodesForPair(genesis, nodeIds))
        join.api.setNodesTo(buildNodesForPair(join, nodeIds))
        cluster.allPairs.forEach { it.waitForMlNodesToLoad() }

        val genesisWeight = 10L
        val joinWeight = 20L
        genesis.api.getNodes().forEach { genesis.setPocWeight(genesisWeight, it.node) }
        join.api.getNodes().forEach { join.setPocWeight(joinWeight, it.node) }

        genesis.waitForStage(EpochStage.SET_NEW_VALIDATORS)

        val allocationsByParticipant = cluster.allPairs.associateWith { pair ->
            pair.api.getNodes().map { node ->
                val timeslotAllocation = node.state.epochMlNodes?.values?.firstOrNull()?.timeslotAllocation
                timeslotAllocation?.getOrNull(1) ?: false
            }
        }
        val hasInferenceAllocation = allocationsByParticipant.values.flatten().any { it }
        val allParticipantsHavePocNodes = allocationsByParticipant.values.all { allocation -> allocation.any { !it } }

        assertThat(hasInferenceAllocation)
            .describedAs("Expected at least one node allocated to inference during PoC")
            .isTrue()
        assertThat(allParticipantsHavePocNodes)
            .describedAs("Expected each participant to keep at least one PoC node")
            .isTrue()

        genesis.waitForNextEpoch()

        val stats = genesis.node.getParticipantCurrentStats()
        val genesisStats = stats.getParticipant(genesis)
        val joinStats = stats.getParticipant(join)

        val expectedGenesisWeight = nodeIds.size * genesisWeight
        val expectedJoinWeight = nodeIds.size * joinWeight

        assertThat(genesisStats?.weight).isEqualTo(expectedGenesisWeight)
        assertThat(joinStats?.weight).isEqualTo(expectedJoinWeight)
    }
}

private fun buildNodesForPair(pair: LocalInferencePair, nodeIds: List<String>): List<InferenceNode> {
    return nodeIds.mapIndexed { index, nodeId ->
        validNode.copy(
            id = nodeId,
            host = "ml-${String.format("%04d", index + 1)}.${pair.name.trimStart('/')}.test",
            models = mapOf(defaultModel to ModelConfig(args = emptyList()))
        )
    }
}

fun checkParticipantWeights(
    appCli: ApplicationCLI,
    genesisParticipantKey: Pubkey2,
    expectedGenesisTokens: Long? = null
) {
    val validators = appCli.getValidators().validators
    val participantCount = validators.size
    
    // Determine expected genesis tokens based on participant count if not specified
    val expectedTokens = expectedGenesisTokens ?: when (participantCount) {
        2 -> 10L // 2 participants: 50% cap results in 10 tokens
        3 -> 13L // 3 participants: 40% cap results in 13 tokens  
        else -> throw AssertionError("Unexpected participant count: $participantCount")
    }
    
    validators.forEach { v ->
        when (v.consensusPubkey.value) {
            genesisParticipantKey.key -> assertThat(v.tokens).isEqualTo(expectedTokens)
            else -> assertThat(v.tokens).isEqualTo(10)
        }
    }
}
