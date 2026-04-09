package inference

import (
	"testing"

	mathsdk "cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func applyDelegationPenalties(
	participants []*types.ActiveParticipant,
	dwc *DelegationWeightCalculator,
	eligibleModels []string,
	modes map[string]map[string]ParticipationMode,
	params DelegationAdjustmentParams,
) {
	acc := NewPenaltyAccumulator(participants)
	AccumulateDelegationPenalties(acc, dwc, eligibleModels, modes, params)
	acc.Apply(participants)
}

func TestAccumulateDelegationPenalties_NoOp(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "alice", Weight: 1000},
	}
	dwc := &DelegationWeightCalculator{}
	params := DelegationAdjustmentParams{
		RRefusal:    mathsdk.LegacyZeroDec(),
		RPenalty:    mathsdk.LegacyZeroDec(),
		RDelegation: mathsdk.LegacyZeroDec(),
	}

	applyDelegationPenalties(participants, dwc, []string{"model1"}, nil, params)
	require.Equal(t, int64(1000), participants[0].Weight)
}

func TestAccumulateDelegationPenalties_DirectNoPenalty(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "alice", Weight: 1000},
	}
	dwc := &DelegationWeightCalculator{}
	modes := map[string]map[string]ParticipationMode{
		"model1": {"alice": ModeDirect},
	}
	params := DelegationAdjustmentParams{
		RRefusal:    mathsdk.LegacyMustNewDecFromStr("0.1"),
		RPenalty:    mathsdk.LegacyMustNewDecFromStr("0.2"),
		RDelegation: mathsdk.LegacyMustNewDecFromStr("0.05"),
	}

	applyDelegationPenalties(participants, dwc, []string{"model1"}, modes, params)
	require.Equal(t, int64(1000), participants[0].Weight)
}

func TestAccumulateDelegationPenalties_RefusePenalty(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "alice", Weight: 1000},
	}
	dwc := &DelegationWeightCalculator{}
	modes := map[string]map[string]ParticipationMode{
		"model1": {"alice": ModeRefuse},
	}
	params := DelegationAdjustmentParams{
		RRefusal:    mathsdk.LegacyMustNewDecFromStr("0.15"),
		RPenalty:    mathsdk.LegacyZeroDec(),
		RDelegation: mathsdk.LegacyZeroDec(),
	}

	applyDelegationPenalties(participants, dwc, []string{"model1"}, modes, params)
	require.Equal(t, int64(850), participants[0].Weight)
}

func TestAccumulateDelegationPenalties_DelegateTransfer(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "alice", Weight: 1000},
		{Index: "bob", Weight: 500},
	}
	dwc := &DelegationWeightCalculator{
		Delegations: map[string]map[string]string{
			"model1": {"alice": "bob"},
		},
	}
	modes := map[string]map[string]ParticipationMode{
		"model1": {
			"alice": ModeDelegate,
			"bob":   ModeDirect,
		},
	}
	params := DelegationAdjustmentParams{
		RRefusal:    mathsdk.LegacyZeroDec(),
		RPenalty:    mathsdk.LegacyZeroDec(),
		RDelegation: mathsdk.LegacyMustNewDecFromStr("0.1"),
	}

	applyDelegationPenalties(participants, dwc, []string{"model1"}, modes, params)

	// alice delegates 10% to bob
	require.Equal(t, int64(900), participants[0].Weight)
	require.Equal(t, int64(600), participants[1].Weight)
}

func TestAccumulateDelegationPenalties_AdditiveAcrossGroups(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "alice", Weight: 1000},
	}
	dwc := &DelegationWeightCalculator{}
	modes := map[string]map[string]ParticipationMode{
		"model1": {"alice": ModeNone},
		"model2": {"alice": ModeNone},
	}
	params := DelegationAdjustmentParams{
		RRefusal:    mathsdk.LegacyZeroDec(),
		RPenalty:    mathsdk.LegacyMustNewDecFromStr("0.1"),
		RDelegation: mathsdk.LegacyZeroDec(),
	}

	applyDelegationPenalties(participants, dwc, []string{"model1", "model2"}, modes, params)

	// Additive: penalty = 0.1 + 0.1 = 0.2, result = 1000 * (1 - 0.2) = 800
	require.Equal(t, int64(800), participants[0].Weight)
}

func TestUnifiedPenalties_DelegationAndBootstrap_Additive(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "alice", Weight: 1000},
	}
	dwc := &DelegationWeightCalculator{}
	delegationModes := map[string]map[string]ParticipationMode{
		"model1": {"alice": ModeNone},
	}
	bootstrapModes := map[string]map[string]BootstrapPenaltyMode{
		"bootstrap1": {"alice": BootstrapPenaltyNone},
	}
	params := DelegationAdjustmentParams{
		RRefusal:    mathsdk.LegacyZeroDec(),
		RPenalty:    mathsdk.LegacyMustNewDecFromStr("0.1"),
		RDelegation: mathsdk.LegacyZeroDec(),
	}

	// Unified accumulator: both sources feed into one accumulator
	acc := NewPenaltyAccumulator(participants)
	AccumulateDelegationPenalties(acc, dwc, []string{"model1"}, delegationModes, params)
	AccumulateBootstrapPenalties(acc, bootstrapModes, nil, params)
	acc.Apply(participants)

	// Additive: 0.1 (delegation) + 0.1 (bootstrap) = 0.2, result = 1000 * 0.8 = 800
	require.Equal(t, int64(800), participants[0].Weight)
}

func TestAccumulatePenalties_CappedAtOne(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "alice", Weight: 1000},
	}
	dwc := &DelegationWeightCalculator{}
	// 11 models, each adding 0.1 penalty = 1.1 total, capped at 1.0
	modes := make(map[string]map[string]ParticipationMode, 11)
	eligibleModels := make([]string, 11)
	for i := 0; i < 11; i++ {
		model := "model" + string(rune('a'+i))
		modes[model] = map[string]ParticipationMode{"alice": ModeNone}
		eligibleModels[i] = model
	}
	params := DelegationAdjustmentParams{
		RRefusal:    mathsdk.LegacyZeroDec(),
		RPenalty:    mathsdk.LegacyMustNewDecFromStr("0.1"),
		RDelegation: mathsdk.LegacyZeroDec(),
	}

	applyDelegationPenalties(participants, dwc, eligibleModels, modes, params)

	// 11 * 0.1 = 1.1, capped at 1.0, weight -> 0
	require.Equal(t, int64(0), participants[0].Weight)
}

func TestResolveBootstrapPenaltyModes_PreEligibleFalse(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "direct", Weight: 50},
		{Index: "delegator", Weight: 40},
		{Index: "intender", Weight: 30},
		{Index: "none", Weight: 20},
	}
	reportByModel := map[string]*types.BootstrapModelPreEligibility{
		"bootstrap-model": {ModelId: "bootstrap-model", PreEligible: false},
	}
	delegations := map[string]map[string]string{
		"bootstrap-model": {"delegator": "direct"},
	}
	intents := map[string]map[string]bool{
		"bootstrap-model": {"intender": true},
	}
	directCommitters := map[string]map[string]bool{
		"bootstrap-model": {"direct": true},
	}

	modes := ResolveBootstrapPenaltyModes(participants, reportByModel, delegations, intents, directCommitters)
	require.Equal(t, BootstrapPenaltyNone, modes["bootstrap-model"]["direct"])
	require.Equal(t, BootstrapPenaltyDelegate, modes["bootstrap-model"]["delegator"])
	require.Equal(t, BootstrapPenaltyIntentOK, modes["bootstrap-model"]["intender"])
	require.Equal(t, BootstrapPenaltyNone, modes["bootstrap-model"]["none"])
}

func TestResolveBootstrapPenaltyModes_PreEligibleTrue(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "direct", Weight: 50},
		{Index: "delegator", Weight: 40},
		{Index: "intender", Weight: 30},
		{Index: "none", Weight: 20},
	}
	reportByModel := map[string]*types.BootstrapModelPreEligibility{
		"bootstrap-model": {ModelId: "bootstrap-model", PreEligible: true},
	}
	delegations := map[string]map[string]string{
		"bootstrap-model": {"delegator": "direct"},
	}
	intents := map[string]map[string]bool{
		"bootstrap-model": {"intender": true},
	}
	directCommitters := map[string]map[string]bool{
		"bootstrap-model": {"direct": true},
	}

	modes := ResolveBootstrapPenaltyModes(participants, reportByModel, delegations, intents, directCommitters)
	require.Equal(t, BootstrapPenaltyDirect, modes["bootstrap-model"]["direct"])
	require.Equal(t, BootstrapPenaltyDelegate, modes["bootstrap-model"]["delegator"])
	require.Equal(t, BootstrapPenaltyIntentMissed, modes["bootstrap-model"]["intender"])
	require.Equal(t, BootstrapPenaltyNone, modes["bootstrap-model"]["none"])
}

func TestAccumulateBootstrapPenalties_MapsIntentMissedAndNone(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "direct", Weight: 100},
		{Index: "delegate", Weight: 80},
		{Index: "intent_missed", Weight: 50},
		{Index: "none", Weight: 50},
	}
	modes := map[string]map[string]BootstrapPenaltyMode{
		"bootstrap-model": {
			"direct":        BootstrapPenaltyDirect,
			"delegate":      BootstrapPenaltyDelegate,
			"intent_missed": BootstrapPenaltyIntentMissed,
			"none":          BootstrapPenaltyNone,
		},
	}
	params := DelegationAdjustmentParams{
		RRefusal:    mathsdk.LegacyMustNewDecFromStr("0.25"),
		RPenalty:    mathsdk.LegacyMustNewDecFromStr("0.5"),
		RDelegation: mathsdk.LegacyZeroDec(),
	}

	acc := NewPenaltyAccumulator(participants)
	AccumulateBootstrapPenalties(acc, modes, nil, params)
	acc.Apply(participants)

	require.Equal(t, int64(100), participants[0].Weight) // Direct: no penalty
	require.Equal(t, int64(80), participants[1].Weight)  // Delegate: no penalty
	require.Equal(t, int64(25), participants[2].Weight)  // IntentMissed: RPenalty 50*0.5=25
	require.Equal(t, int64(25), participants[3].Weight)  // None: RPenalty 50*0.5=25
}

func TestAccumulateDelegationPenalties_MixedModesAcrossModels(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "alice", Weight: 1000},
	}
	dwc := &DelegationWeightCalculator{}
	modes := map[string]map[string]ParticipationMode{
		"model1": {"alice": ModeRefuse},
		"model2": {"alice": ModeNone},
	}
	params := DelegationAdjustmentParams{
		RRefusal:    mathsdk.LegacyMustNewDecFromStr("0.05"),
		RPenalty:    mathsdk.LegacyMustNewDecFromStr("0.1"),
		RDelegation: mathsdk.LegacyZeroDec(),
	}

	applyDelegationPenalties(participants, dwc, []string{"model1", "model2"}, modes, params)

	// Additive: 0.05 (refuse) + 0.1 (none) = 0.15, result = 1000 * 0.85 = 850
	require.Equal(t, int64(850), participants[0].Weight)
}
