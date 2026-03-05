// Package gossip propagates nonce awareness and signatures across subnet hosts.
//
// Each nonce tracks one state in gossip:
//
//   - SEEN: received via gossip or AfterRequest, aware of its existence
//
// A nonce can be SEEN but not yet applied when a host learns about it via gossip
// (just nonce + hash + sig) but hasn't received the actual diffs yet. This
// happens when the user contacts host_0, which gossips to host_2, but the
// user hasn't sent diffs to host_2 directly. The host knows the nonce exists
// but cannot sign it without the state.
//
// Primary propagation is user-driven (diffs sent round-robin). Gossip is
// secondary: it detects gaps, amplifies awareness, and enables self-healing
// when hosts fall behind.
//
// Flows:
//
// AfterRequest: called after a host processes a user request. Stores nonce
// in seen map, records lastAfterReqNonce, sends (nonce, hash, sig, slot) to
// K peers.
//
// OnNonceReceived: handles incoming gossip. Checks for equivocation (same
// nonce, different hash -> error). Marks SEEN, stores sender sig. Forwards
// to K peers (amplification). For already-seen nonces, always tries to
// accumulate the signature via the SigAccumulator callback (the accumulator
// itself validates whether the nonce is ready).
//
// Rebroadcast (every 30s): finds SEEN nonces older than StaleTTL (120s) and
// re-sends to K peers so other hosts learn.
//
// Recovery (every 60s): if highest SEEN > lastAfterReqNonce (the nonce from
// the most recent AfterRequest) and no recent user contact (RecoveryDelay),
// picks a random peer and fetches diffs via DiffFetcher. Applies them via
// StateUpdater, which calls StateMachine.ApplyDiff for each diff. ApplyDiff
// verifies the user's signature on every diff, so a malicious peer cannot
// serve forged diffs -- they fail the user sig check. After applying, the
// host signs each nonce and gossips its own sigs.
//
// Gossip does NOT carry diffs (only nonce/hash/sig). It does not allow signing
// without having the actual diffs. It does not replace user propagation.
package gossip
