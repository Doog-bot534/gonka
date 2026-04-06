package inference

import (
	"sort"

	mathsdk "cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/types"
)

// ParticipationMode defines how a participant relates to a model group.
type ParticipationMode int

const (
	ModeDirect   ParticipationMode = iota // Member of the group (has MLNode deployed)
	ModeIntent                            // Declared intent to deploy but didn't participate in PoC
	ModeRefuse                            // Explicitly refused delegation
	ModeDelegate                          // Delegates consensus weight to a group member
	ModeNone                              // No valid delegation, no refusal, no intent
)

// GroupData holds per-model group information.
type GroupData struct {
	Members          []string          // addresses of direct group members
	MemberPocWeights map[string]int64  // member -> pocWeight in this group
	ConsensusKoeff   mathsdk.LegacyDec // coefficient for this model
	IsInitialGroup   bool              // exempt from cap
}

// WeightParams holds governance parameters for delegation.
type WeightParams struct {
	WThreshold mathsdk.LegacyDec // min fraction of total weight from members for eligibility
	VMin       int64             // min hosts with non-zero consensus weight
	CapFactor  mathsdk.LegacyDec // max group weight as multiple of members' weight in other groups
}

// DelegationWeightCalculator sits above PoCWeightCalculator and handles
// cross-group concerns: eligibility, caps, consensus weight, delegation modes,
// and per-group voting power.
type DelegationWeightCalculator struct {
	Groups             map[string]*GroupData        // model_id -> group data
	ConsensusWeights   map[string]int64             // participant -> ActiveParticipant.Weight from N-1
	TotalNetworkWeight int64                        // sum(ConsensusWeights)
	Delegations        map[string]map[string]string // model_id -> (delegator -> delegate_to)
	Refusals           map[string]map[string]bool   // model_id -> (participant -> true)
	Intents            map[string]map[string]bool   // model_id -> (participant -> true)
	Params             WeightParams
	Logger             types.InferenceLogger
}

func buildWeightParams(params types.Params) WeightParams {
	wp := WeightParams{
		WThreshold: mathsdk.LegacyZeroDec(),
		VMin:       0,
		CapFactor:  mathsdk.LegacyZeroDec(),
	}
	if params.DelegationParams != nil {
		dp := params.DelegationParams
		wp.WThreshold = protoDecToLegacy(dp.WThreshold)
		wp.VMin = dp.VMin
		wp.CapFactor = protoDecToLegacy(dp.CapFactor)
	}
	return wp
}

// IsGovernanceApproved checks if a model group exists with a defined coefficient.
func (wc *DelegationWeightCalculator) IsGovernanceApproved(modelID string) bool {
	g, ok := wc.Groups[modelID]
	if !ok {
		return false
	}
	return g.ConsensusKoeff.IsPositive()
}

// MeetsWeightThreshold checks if members' consensus weight >= W_threshold * total network weight.
func (wc *DelegationWeightCalculator) MeetsWeightThreshold(modelID string) bool {
	if wc.Params.WThreshold.IsZero() {
		return true
	}
	g, ok := wc.Groups[modelID]
	if !ok {
		return false
	}
	memberWeight := int64(0)
	for _, m := range g.Members {
		memberWeight += wc.ConsensusWeights[m]
	}
	threshold := wc.Params.WThreshold.MulInt64(wc.TotalNetworkWeight).TruncateInt64()
	return memberWeight >= threshold
}

// MeetsMinHosts checks if at least V_min members have non-zero consensus weight.
func (wc *DelegationWeightCalculator) MeetsMinHosts(modelID string) bool {
	if wc.Params.VMin <= 0 {
		return true
	}
	g, ok := wc.Groups[modelID]
	if !ok {
		return false
	}
	count := int64(0)
	for _, m := range g.Members {
		if wc.ConsensusWeights[m] > 0 {
			count++
		}
	}
	return count >= wc.Params.VMin
}

// IsGroupPreEligible checks all pre-eligibility conditions.
func (wc *DelegationWeightCalculator) IsGroupPreEligible(modelID string) bool {
	return wc.IsGovernanceApproved(modelID) &&
		wc.MeetsWeightThreshold(modelID) &&
		wc.MeetsMinHosts(modelID)
}

func (wc *DelegationWeightCalculator) ProjectedReachableVotingPower(modelID string) int64 {
	g, ok := wc.Groups[modelID]
	if !ok {
		return 0
	}

	memberSet := make(map[string]bool, len(g.Members))
	reachable := int64(0)
	for _, m := range g.Members {
		memberSet[m] = true
		reachable += wc.ConsensusWeights[m]
	}

	for delegator, target := range wc.Delegations[modelID] {
		if !memberSet[target] || memberSet[delegator] {
			continue
		}
		reachable += wc.ConsensusWeights[delegator]
	}

	return reachable
}

func (wc *DelegationWeightCalculator) MeetsReachabilityThreshold(modelID string) bool {
	if wc.TotalNetworkWeight <= 0 {
		return false
	}
	return wc.ProjectedReachableVotingPower(modelID)*3 > wc.TotalNetworkWeight*2
}

// IsGroupEligible checks post-PoC eligibility. At least V_min members must have
// pocWeight > 0, plus governance approval and weight threshold.
func (wc *DelegationWeightCalculator) IsGroupEligible(modelID string) bool {
	if !wc.IsGovernanceApproved(modelID) {
		return false
	}
	if !wc.MeetsWeightThreshold(modelID) {
		return false
	}
	g := wc.Groups[modelID]
	if wc.Params.VMin > 0 {
		count := int64(0)
		for _, m := range g.Members {
			if g.MemberPocWeights[m] > 0 {
				count++
			}
		}
		if count < wc.Params.VMin {
			return false
		}
	}
	return true
}

// ResolveGroupParticipation returns participation mode for each participant
// with positive N-1 consensus weight, for one model group.
func (wc *DelegationWeightCalculator) ResolveGroupParticipation(modelID string) map[string]ParticipationMode {
	g, ok := wc.Groups[modelID]
	if !ok {
		return nil
	}

	memberSet := make(map[string]bool, len(g.Members))
	for _, m := range g.Members {
		memberSet[m] = true
	}

	modes := make(map[string]ParticipationMode)
	for p, w := range wc.ConsensusWeights {
		if w <= 0 {
			continue
		}
		if memberSet[p] {
			modes[p] = ModeDirect
			continue
		}
		// Check intent (INTENT > REFUSE > DELEGATE > NONE)
		if intents, ok := wc.Intents[modelID]; ok && intents[p] {
			modes[p] = ModeIntent
			continue
		}
		if refusals, ok := wc.Refusals[modelID]; ok && refusals[p] {
			modes[p] = ModeRefuse
			continue
		}
		if delegations, ok := wc.Delegations[modelID]; ok {
			if target, hasDelegation := delegations[p]; hasDelegation {
				// Delegation target must be a member with positive consensus weight
				if memberSet[target] && wc.ConsensusWeights[target] > 0 {
					modes[p] = ModeDelegate
				} else {
					// Invalid target: not a member or zero weight -> NONE
					modes[p] = ModeNone
				}
				continue
			}
		}
		modes[p] = ModeNone
	}
	return modes
}

// ComputeGroupCap returns the maximum consensus weight this group can contribute.
// Returns -1 (uncapped) for initial groups or when cap_factor is zero.
func (wc *DelegationWeightCalculator) ComputeGroupCap(modelID string) int64 {
	g, ok := wc.Groups[modelID]
	if !ok {
		return 0
	}
	if g.IsInitialGroup || wc.Params.CapFactor.IsZero() {
		return -1 // uncapped
	}
	// cap = CapFactor * sum(member's consensus weight from other eligible groups)
	// We approximate "from other groups" as total N-1 consensus weight minus
	// this group's N-1 contribution (koeff * pocWeight).
	otherGroupWeight := int64(0)
	for _, m := range g.Members {
		totalWeight := wc.ConsensusWeights[m]
		thisGroupContrib := g.ConsensusKoeff.MulInt64(g.MemberPocWeights[m]).TruncateInt64()
		other := totalWeight - thisGroupContrib
		if other > 0 {
			otherGroupWeight += other
		}
	}
	cap := wc.Params.CapFactor.MulInt64(otherGroupWeight).TruncateInt64()
	if cap <= 0 {
		return 0
	}
	return cap
}

// EligibleGroups returns a sorted list of eligible model IDs.
func (wc *DelegationWeightCalculator) EligibleGroups() []string {
	var eligible []string
	for modelID := range wc.Groups {
		if wc.IsGroupEligible(modelID) {
			eligible = append(eligible, modelID)
		}
	}
	sort.Strings(eligible)
	return eligible
}

// ComputeConsensusWeights produces final ActiveParticipant.Weight for each
// participant across all eligible groups, applying coefficients and caps.
func (wc *DelegationWeightCalculator) ComputeConsensusWeights(eligibleGroups []string) map[string]int64 {
	result := make(map[string]int64)

	for _, modelID := range eligibleGroups {
		g := wc.Groups[modelID]
		if g == nil {
			continue
		}

		// Compute raw group total: koeff * sum(pocWeight)
		rawContributions := make(map[string]int64)
		rawTotal := int64(0)
		for _, m := range g.Members {
			contrib := g.ConsensusKoeff.MulInt64(g.MemberPocWeights[m]).TruncateInt64()
			rawContributions[m] = contrib
			rawTotal += contrib
		}

		// Apply cap
		cap := wc.ComputeGroupCap(modelID)
		scaleFactor := mathsdk.LegacyOneDec()
		if cap >= 0 && rawTotal > cap && rawTotal > 0 {
			scaleFactor = mathsdk.LegacyNewDec(cap).Quo(mathsdk.LegacyNewDec(rawTotal))
		}

		// Add scaled contributions to result
		for m, contrib := range rawContributions {
			scaled := scaleFactor.MulInt64(contrib).TruncateInt64()
			result[m] += scaled
		}
	}

	return result
}

// ComputeGroupVotingPowers resolves delegation for one model group and returns
// per-DIRECT-member voting power. A DIRECT member's voting power includes their
// own consensus weight plus all consensus weight delegated to them.
//
// finalWeights are the post-adjustment consensus weights (after delegation adjustment,
// collateral, and power capping).
//
// Returns map[participant_address]voting_power. Only DIRECT members get entries.
func (wc *DelegationWeightCalculator) ComputeGroupVotingPowers(
	modelID string,
	modes map[string]ParticipationMode,
	finalWeights map[string]int64,
) map[string]int64 {
	g, ok := wc.Groups[modelID]
	if !ok {
		return nil
	}

	// Start with DIRECT members' own final weight
	votingPower := make(map[string]int64)
	for _, m := range g.Members {
		if modes[m] == ModeDirect {
			votingPower[m] = finalWeights[m]
		}
	}

	// Add delegated weight
	delegations := wc.Delegations[modelID]
	for delegator, target := range delegations {
		if modes[delegator] != ModeDelegate {
			continue
		}
		if modes[target] == ModeDirect {
			votingPower[target] += finalWeights[delegator]
		}
	}

	return votingPower
}
