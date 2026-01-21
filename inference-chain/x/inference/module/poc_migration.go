package inference

import (
	"github.com/productscience/inference/x/inference/types"
)

// MigrationState represents the current V1/V2 mode based on governance parameters.
type MigrationState int

const (
	ModeFullV1    MigrationState = iota // poc_v2_enabled=false, confirmation_poc_v2_enabled=false
	ModeMigration                       // poc_v2_enabled=false, confirmation_poc_v2_enabled=true
	ModeFullV2                          // poc_v2_enabled=true (confirmation_poc_v2_enabled must also be true)
)

// Default threshold for auto-switch (75%)
const (
	DefaultAutoSwitchParticipantThreshold = 0.75
	DefaultAutoSwitchCoverageThreshold    = 0.75
)

// GetMigrationState determines current mode from params.
// Returns ModeFullV2 for invalid combinations (poc_v2=true, confirmation_v2=false).
func GetMigrationState(pocV2Enabled, confirmationPocV2Enabled bool) MigrationState {
	if pocV2Enabled {
		return ModeFullV2
	}
	if confirmationPocV2Enabled {
		return ModeMigration
	}
	return ModeFullV1
}

// GetMigrationStateFromParams extracts migration state from PocParams.
func GetMigrationStateFromParams(params *types.PocParams) MigrationState {
	if params == nil {
		return ModeFullV2 // default
	}
	return GetMigrationState(params.PocV2Enabled, params.ConfirmationPocV2Enabled)
}

// ValidationCoverage represents participant validation results for auto-switch evaluation.
type ValidationCoverage struct {
	ParticipantAddress string
	TotalWeight        uint64  // Total validator weight that could vote
	VotedWeight        int64   // Weight of validators that actually voted
	Coverage           float64 // VotedWeight/TotalWeight (0.0 to 1.0)
}

// CalculateCoverages computes validation coverage for all participants.
// validations: map[participantAddress][]PoCValidationV2
// validatorWeights: map[validatorAddress]weight
func CalculateCoverages(
	validations map[string][]types.PoCValidationV2,
	validatorWeights map[string]int64,
) []ValidationCoverage {
	totalWeight := calculateTotalWeight(validatorWeights)
	if totalWeight == 0 {
		return nil
	}

	coverages := make([]ValidationCoverage, 0, len(validations))
	for participantAddr, vals := range validations {
		votedWeight := int64(0)
		for _, v := range vals {
			if w, ok := validatorWeights[v.ValidatorParticipantAddress]; ok {
				votedWeight += w
			}
		}

		coverage := float64(votedWeight) / float64(totalWeight)
		coverages = append(coverages, ValidationCoverage{
			ParticipantAddress: participantAddr,
			TotalWeight:        totalWeight,
			VotedWeight:        votedWeight,
			Coverage:           coverage,
		})
	}

	return coverages
}

// ShouldAutoSwitch evaluates if conditions are met for automatic V2 switch.
// Returns true if >= participantThreshold of participants have >= coverageThreshold coverage.
func ShouldAutoSwitch(coverages []ValidationCoverage, participantThreshold, coverageThreshold float64) bool {
	if len(coverages) == 0 {
		return false
	}

	sufficientCount := 0
	for _, c := range coverages {
		if c.Coverage >= coverageThreshold {
			sufficientCount++
		}
	}

	ratio := float64(sufficientCount) / float64(len(coverages))
	return ratio >= participantThreshold
}

// AutoSwitchResult contains the result of auto-switch evaluation.
type AutoSwitchResult struct {
	ShouldSwitch       bool
	TotalParticipants  int
	SufficientCoverage int
	ParticipantRatio   float64
	Coverages          []ValidationCoverage
}

// EvaluateAutoSwitch performs full auto-switch evaluation with detailed results.
func EvaluateAutoSwitch(
	coverages []ValidationCoverage,
	participantThreshold, coverageThreshold float64,
) AutoSwitchResult {
	result := AutoSwitchResult{
		TotalParticipants: len(coverages),
		Coverages:         coverages,
	}

	if len(coverages) == 0 {
		return result
	}

	for _, c := range coverages {
		if c.Coverage >= coverageThreshold {
			result.SufficientCoverage++
		}
	}

	result.ParticipantRatio = float64(result.SufficientCoverage) / float64(result.TotalParticipants)
	result.ShouldSwitch = result.ParticipantRatio >= participantThreshold

	return result
}
