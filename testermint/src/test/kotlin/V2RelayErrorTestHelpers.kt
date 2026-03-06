import com.google.gson.annotations.SerializedName
import com.productscience.cosmosJson
import org.bouncycastle.asn1.ASN1Integer
import org.bouncycastle.asn1.ASN1Primitive
import org.bouncycastle.asn1.ASN1Sequence
import org.bouncycastle.asn1.sec.SECNamedCurves
import org.bouncycastle.crypto.params.ECDomainParameters
import org.bouncycastle.crypto.params.ECPublicKeyParameters
import org.bouncycastle.crypto.signers.ECDSASigner
import java.io.ByteArrayOutputStream
import java.math.BigInteger
import java.nio.ByteBuffer
import java.security.MessageDigest
import java.util.Base64

private const val RELAY_ERROR_SIGN_DOMAIN = "v2_relay_error_sig_v1"

data class V2RelayErrorArtifact(
    @SerializedName("escrow_id")
    val escrowId: String,
    @SerializedName("request_id")
    val requestId: String,
    @SerializedName("intended_executor_address")
    val intendedExecutorAddress: String,
    @SerializedName("relay_address")
    val relayAddress: String,
    @SerializedName("failure_code")
    val failureCode: String,
    @SerializedName("relay_signer_address")
    val relaySignerAddress: String,
    @SerializedName("relay_signer_pubkey")
    val relaySignerPubKey: String,
    @SerializedName("relay_signature")
    val relaySignature: String,
    @SerializedName("timestamp")
    val timestamp: Long,
)

data class V2RelayErrorEnvelope(
    val error: V2RelayErrorArtifact?,
)

fun parseV2RelayErrorArtifact(rawBody: String): V2RelayErrorArtifact {
    val envelope = cosmosJson.fromJson(rawBody, V2RelayErrorEnvelope::class.java)
    return requireNotNull(envelope.error) { "Missing relay error artifact in body: $rawBody" }
}

fun verifyV2RelayErrorArtifactSignature(artifact: V2RelayErrorArtifact): Boolean {
    val signingPayload = buildRelayErrorSigningPayload(artifact)
    return verifySecp256k1Signature(
        signingPayload = signingPayload,
        pubKeyBytes = Base64.getDecoder().decode(artifact.relaySignerPubKey),
        signatureBytes = Base64.getDecoder().decode(artifact.relaySignature),
    )
}

private fun buildRelayErrorSigningPayload(artifact: V2RelayErrorArtifact): ByteArray {
    val preimage = ByteArrayOutputStream()
    writeLengthPrefixedString(preimage, RELAY_ERROR_SIGN_DOMAIN)
    writeLengthPrefixedString(preimage, artifact.escrowId)
    writeLengthPrefixedString(preimage, artifact.requestId)
    writeLengthPrefixedString(preimage, artifact.intendedExecutorAddress)
    writeLengthPrefixedString(preimage, artifact.relayAddress)
    writeLengthPrefixedString(preimage, artifact.failureCode)
    writeLengthPrefixedString(preimage, artifact.relaySignerAddress)
    writeLengthPrefixedString(preimage, artifact.relaySignerPubKey)
    writeInt64(preimage, artifact.timestamp)
    return sha256Hex(preimage.toByteArray()).toByteArray(Charsets.UTF_8)
}

private fun verifySecp256k1Signature(signingPayload: ByteArray, pubKeyBytes: ByteArray, signatureBytes: ByteArray): Boolean {
    return try {
        val curve = SECNamedCurves.getByName("secp256k1")
        val domain = ECDomainParameters(curve.curve, curve.g, curve.n, curve.h)
        val publicPoint = curve.curve.decodePoint(pubKeyBytes)
        val publicKey = ECPublicKeyParameters(publicPoint, domain)
        val signer = ECDSASigner()
        signer.init(false, publicKey)
        val signatureParts = parseDerSignature(signatureBytes) ?: return false
        val sha256 = MessageDigest.getInstance("SHA-256")
        val maybeHexDecoded = runCatching { hexToBytes(String(signingPayload, Charsets.UTF_8)) }.getOrNull()
        val candidatePayloads = listOfNotNull(
            signingPayload,
            sha256.digest(signingPayload),
            maybeHexDecoded,
            maybeHexDecoded?.let { sha256.digest(it) },
        ).distinctBy { it.joinToString(",") }

        for (candidate in candidatePayloads) {
            if (signer.verifySignature(candidate, signatureParts.first, signatureParts.second)) {
                return true
            }
        }
        false
    } catch (_: Exception) {
        false
    }
}

private fun parseDerSignature(signatureBytes: ByteArray): Pair<BigInteger, BigInteger>? {
    if (signatureBytes.size == 64) {
        val r = BigInteger(1, signatureBytes.copyOfRange(0, 32))
        val s = BigInteger(1, signatureBytes.copyOfRange(32, 64))
        return r to s
    }
    return try {
        val sequence = ASN1Primitive.fromByteArray(signatureBytes) as? ASN1Sequence ?: return null
        if (sequence.size() != 2) {
            return null
        }
        val r = (sequence.getObjectAt(0) as ASN1Integer).positiveValue
        val s = (sequence.getObjectAt(1) as ASN1Integer).positiveValue
        r to s
    } catch (_: Exception) {
        null
    }
}

private fun sha256Hex(input: ByteArray): String =
    MessageDigest.getInstance("SHA-256").digest(input).joinToString("") { "%02x".format(it) }

private fun hexToBytes(hex: String): ByteArray {
    val clean = hex.trim()
    require(clean.length % 2 == 0) { "hex length must be even" }
    return ByteArray(clean.length / 2) { idx ->
        clean.substring(idx * 2, idx * 2 + 2).toInt(16).toByte()
    }
}

private fun writeLengthPrefixedString(output: ByteArrayOutputStream, value: String) {
    val bytes = value.toByteArray(Charsets.UTF_8)
    output.write(ByteBuffer.allocate(4).putInt(bytes.size).array())
    output.write(bytes)
}

private fun writeInt64(output: ByteArrayOutputStream, value: Long) {
    output.write(ByteBuffer.allocate(8).putLong(value).array())
}
