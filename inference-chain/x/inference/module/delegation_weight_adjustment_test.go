package inference

import (
	"testing"

	mathsdk "cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestApplyDelegationWeightAdjustment_NoOp(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "alice", Weight: 1000},
	}
	dwc := &DelegationWeightCalculator{}
	params := DelegationAdjustmentParams{
		RRefusal:    mathsdk.LegacyZeroDec(),
		RPenalty:    mathsdk.LegacyZeroDec(),
		RDelegation: mathsdk.LegacyZeroDec(),
	}

	ApplyDelegationWeightAdjustment(participants, dwc, []string{"model1"}, nil, params)
	require.Equal(t, int64(1000), participants[0].Weight)
}

func TestApplyDelegationWeightAdjustment_DirectNoPenalty(t *testing.T) {
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

	ApplyDelegationWeightAdjustment(participants, dwc, []string{"model1"}, modes, params)
	require.Equal(t, int64(1000), participants[0].Weight)
}

func TestApplyDelegationWeightAdjustment_RefusePenalty(t *testing.T) {
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

	ApplyDelegationWeightAdjustment(participants, dwc, []string{"model1"}, modes, params)
	require.Equal(t, int64(850), participants[0].Weight)
}

func TestApplyDelegationWeightAdjustment_DelegateTransfer(t *testing.T) {
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

	ApplyDelegationWeightAdjustment(participants, dwc, []string{"model1"}, modes, params)

	// alice delegates 10% to bob
	require.Equal(t, int64(900), participants[0].Weight)
	require.Equal(t, int64(600), participants[1].Weight)
}

func TestApplyDelegationWeightAdjustment_CompoundsAcrossGroups(t *testing.T) {
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

	ApplyDelegationWeightAdjustment(participants, dwc, []string{"model1", "model2"}, modes, params)

	// 1000 * 0.9 = 900 after model1, then 900 * 0.9 = 810 after model2
	require.Equal(t, int64(810), participants[0].Weight)
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

func TestApplyBootstrapPenaltyAdjustment_MapsIntentMissedAndNone(t *testing.T) {
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

	ApplyBootstrapPenaltyAdjustment(participants, modes, params)

	require.Equal(t, int64(100), participants[0].Weight)
	require.Equal(t, int64(80), participants[1].Weight)
	require.Equal(t, int64(38), participants[2].Weight)
	require.Equal(t, int64(25), participants[3].Weight)
}
