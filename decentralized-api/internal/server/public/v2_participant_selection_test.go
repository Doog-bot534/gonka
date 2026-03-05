package public

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestResolveV2ResponsibleParticipantCount(t *testing.T) {
	require.Equal(t, int(types.DefaultV2ResponsibleParticipantsCount), resolveV2ResponsibleParticipantCount(0))
	require.Equal(t, 7, resolveV2ResponsibleParticipantCount(7))
}

func TestSelectResponsibleParticipantsDeterministic_IsStableAndOrderIndependent(t *testing.T) {
	participantsA := []weightedParticipant{
		{address: "participant-c", weight: 5},
		{address: "participant-a", weight: 10},
		{address: "participant-b", weight: 7},
		{address: "participant-d", weight: 3},
	}
	participantsB := []weightedParticipant{
		{address: "participant-b", weight: 7},
		{address: "participant-d", weight: 3},
		{address: "participant-c", weight: 5},
		{address: "participant-a", weight: 10},
	}

	responsibleA1, err := selectResponsibleParticipantsDeterministic(participantsA, 3, "escrow-1", 42)
	require.NoError(t, err)
	responsibleA2, err := selectResponsibleParticipantsDeterministic(participantsA, 3, "escrow-1", 42)
	require.NoError(t, err)
	responsibleB, err := selectResponsibleParticipantsDeterministic(participantsB, 3, "escrow-1", 42)
	require.NoError(t, err)

	require.Equal(t, responsibleA1, responsibleA2)
	require.Equal(t, responsibleA1, responsibleB)
	require.Len(t, responsibleA1, 3)
	require.NotEqual(t, responsibleA1[0], responsibleA1[1])
	require.NotEqual(t, responsibleA1[1], responsibleA1[2])
	require.NotEqual(t, responsibleA1[0], responsibleA1[2])
}

func TestSelectResponsibleParticipantsDeterministic_IgnoresZeroWeightAndClampsCount(t *testing.T) {
	responsibleParticipants, err := selectResponsibleParticipantsDeterministic([]weightedParticipant{
		{address: "participant-a", weight: 0},
		{address: "participant-b", weight: 11},
	}, 4, "escrow-1", 1)
	require.NoError(t, err)
	require.Equal(t, []string{"participant-b"}, responsibleParticipants)
}

func TestSelectResponsibleParticipantsDeterministic_RejectsNoEligibleParticipants(t *testing.T) {
	_, err := selectResponsibleParticipantsDeterministic([]weightedParticipant{
		{address: "participant-a", weight: 0},
	}, 1, "escrow-1", 1)
	require.Error(t, err)
}
