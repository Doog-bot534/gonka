package types

// DefaultSessionConfig returns the v0.2.11 session config (no fees).
func DefaultSessionConfig(groupSize int) SessionConfig {
	return SessionConfig{
		RefusalTimeout:   60,
		ExecutionTimeout: 1200,
		TokenPrice:       1,
		VoteThreshold:    uint32(groupSize) / 2,
		ValidationRate:   5000,
	}
}

// DefaultSessionConfigV0212 returns the v0.2.12 session config with fee fields.
func DefaultSessionConfigV0212(groupSize int) SessionConfig {
	cfg := DefaultSessionConfig(groupSize)
	cfg.CreateSubnetFee = 10_000
	cfg.FeePerNonce = 1_000
	return cfg
}

// SessionConfigForVersion returns the default config for the given protocol version.
func SessionConfigForVersion(groupSize int, version ProtocolVersion) SessionConfig {
	switch version {
	case ProtocolV0212:
		return DefaultSessionConfigV0212(groupSize)
	default:
		return DefaultSessionConfig(groupSize)
	}
}

// SessionConfigWithPrice returns a session config with a custom token price.
// tokenPrice == 0 is treated as 1 for backward compatibility.
func SessionConfigWithPrice(groupSize int, tokenPrice uint64) SessionConfig {
	cfg := DefaultSessionConfig(groupSize)
	if tokenPrice > 0 {
		cfg.TokenPrice = tokenPrice
	}
	return cfg
}

// SessionConfigWithPriceAndVersion returns a versioned session config with a custom token price.
func SessionConfigWithPriceAndVersion(groupSize int, tokenPrice uint64, version ProtocolVersion) SessionConfig {
	cfg := SessionConfigForVersion(groupSize, version)
	if tokenPrice > 0 {
		cfg.TokenPrice = tokenPrice
	}
	return cfg
}
