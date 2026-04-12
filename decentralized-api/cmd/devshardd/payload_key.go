package main

import "fmt"

// payloadKey is the namespaced key devshardd uses to store and retrieve
// prompt/response pairs in the shared payload store. It intentionally matches
// the format used by dapi's in-process adapter (devshard:<escrowID>:<inferenceID>)
// so devshardd and dapi can coexist against the same storage if needed.
func payloadKey(escrowID string, inferenceID uint64) string {
	return fmt.Sprintf("devshard:%s:%d", escrowID, inferenceID)
}
