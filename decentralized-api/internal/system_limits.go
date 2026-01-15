package internal

import (
	"fmt"
	"syscall"

	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

// ConfigureSystemLimitsForValidators updates RLIMIT_NOFILE based on active validators count.
// This avoids chain queries during startup and can be called when epoch data is cached.
func ConfigureSystemLimitsForValidators(validatorCount uint64) {
	// Default usage: ~2000 for system/db/etc + connections
	baseFDs := uint64(2048)
	validatorMultiplier := uint64(5) // 1 for gossip out, 1 for gossip in, + margins

	needed := baseFDs + (validatorCount * validatorMultiplier)
	if needed < 65535 {
		needed = 65535
	}

	var rLimit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit); err != nil {
		fmt.Printf("Error getting rlimit: %s\n", err)
		return
	}

	if rLimit.Cur < needed {
		target := needed
		// Try to raise max if target is higher than max
		if target > rLimit.Max {
			rLimit.Max = target
		}
		rLimit.Cur = target

		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rLimit); err != nil {
			// Fallback: set to whatever the existing max is
			var rLimit2 syscall.Rlimit
			_ = syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit2)
			rLimit2.Cur = rLimit2.Max
			_ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rLimit2)
			logging.Warn("Failed to set requested FD limit, fell back to max", types.System,
				"requested", target, "actual", rLimit2.Cur, "error", err)
		} else {
			logging.Info("Updated RLIMIT_NOFILE", types.System,
				"validator_count", validatorCount,
				"limit", rLimit.Cur)
		}
	} else {
		logging.Debug("RLIMIT_NOFILE already sufficient", types.System,
			"validator_count", validatorCount,
			"limit", rLimit.Cur)
	}
}
