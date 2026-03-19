package com.productscience

import com.productscience.data.*
import org.jline.reader.EndOfFileException
import org.jline.reader.LineReaderBuilder
import org.jline.reader.UserInterruptException
import org.jline.terminal.TerminalBuilder
import org.tinylog.configuration.Configuration
import javax.script.ScriptEngineManager

val noRestrictionsSpec = spec<AppState> {
    this[AppState::restrictions] = spec<RestrictionsState> {
        this[RestrictionsState::params] = spec<RestrictionsParams> {
            this[RestrictionsParams::restrictionEndBlock] = 0L
            this[RestrictionsParams::emergencyTransferExemptions] = emptyList<EmergencyTransferExemption>()
            this[RestrictionsParams::exemptionUsageTracking] = emptyList<ExemptionUsageEntry>()
        }
    }
}

val noRestrictionsConfig = inferenceConfig.copy(
    genesisSpec = inferenceConfig.genesisSpec?.merge(noRestrictionsSpec) ?: noRestrictionsSpec
)

open class ReplSession() {
    protected lateinit var cluster: LocalCluster
    protected lateinit var genesis: LocalInferencePair

    fun initialize() {
        println("Initializing cluster (please wait a couple of minutes)")
        val (cluster, genesis) = initCluster(config = noRestrictionsConfig, reboot = true)
        this.cluster = cluster
        this.genesis = genesis

        genesis.waitForNextEpoch()

        cluster.allPairs.forEach {
            it.mock?.setInferenceResponse(
                """{"id":"test","object":"chat.completion","created":0,"model":"$defaultModel","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}"""
            )
        }
    }
}

open class SubnetSession(
    val escrowId: Long = 1,
    val userKeyName: String = "subnet-proxy-user",
) : ReplSession() {
    protected lateinit var handle: LocalInferencePair.SubnetProxyHandle
    protected lateinit var userAddress: String

    fun sendChatCompletion() {
        println("Sending chat completions via proxy")
        val response = this.genesis.sendChatCompletion(handle.proxyUrl, defaultModel, "test prompt")
        println(response)
    }

    fun fundUser(fundAmount: Long = 10_000_000_000L) {
        val transferResp = this.genesis.submitTransaction(
            listOf("bank", "send", this.genesis.node.getColdAddress(), this.userAddress, "${fundAmount}${genesis.config.denom}")
        )
    }

    fun startSubnet(escrowAmount: Long = 7_000_000_000L) {
        super.initialize()

        println("Creating separate user account")
        val userKey = this.genesis.node.createKey(this.userKeyName)
        this.userAddress = userKey.address
        this.fundUser()

        this.genesis.waitForNextInferenceWindow()

        println("Creating subnet escrow from user account")
        val txResp = this.genesis.createSubnetEscrow(escrowAmount, from = this.userKeyName)
        assert(txResp.code == 0)

        println("Starting subnet proxy")
        this.handle = this.genesis.startSubnetProxy(this.escrowId, keyName = this.userKeyName)
    }

    fun stopSubnet() {
        try {
            println("Finalizing via proxy")
            val result = this.genesis.finalizeSubnetProxy(handle.proxyUrl)

            println("Submitting settlement from user account")
            val settleResp = this.genesis.settleSubnetEscrow(result.rawJson, from = this.userKeyName)
            assert(settleResp.code == 0)

            println("Verifying escrow settled")
            val escrow = this.genesis.node.querySubnetEscrow(1)
            assert(escrow.escrow?.settled == true)
        } finally {
            println("Stopping subnet proxy")
            this.genesis.stopSubnetProxy(this.escrowId)
        }
    }
}

fun main() {
    Configuration.set("writer.level", "off")

    val terminal = TerminalBuilder.builder()
        .system(true)
        .jna(true)
        .jansi(true)
        .build()
    val reader = LineReaderBuilder.builder()
        .terminal(terminal)
        .build()

    val engine = ScriptEngineManager().getEngineByName("kotlin")
    engine.eval("import com.productscience.*")

    println("Enter Kotlin code (or type 'exit' to quit)")
    while (true) {
        val input = try {
            reader.readLine("> ")
        } catch (_: UserInterruptException) {
            println("^C")
            continue
        } catch (_: EndOfFileException) {
            println("^D")
            break
        }

        if (input == "exit") {
            break
        }

        try {
            val result = engine.eval(input)
            if (result != null) {
                println(result)
            }
        } catch (e: Exception) {
            println(e.message)
        }
    }
}
