package public

import "testing"

func TestHasV2MissedInferenceQuorum(t *testing.T) {
	testCases := []struct {
		name                string
		totalParticipants   int
		validRelayArtifacts int
		expectedHasQuorum   bool
	}{
		{name: "no participants", totalParticipants: 0, validRelayArtifacts: 0, expectedHasQuorum: false},
		{name: "one of one", totalParticipants: 1, validRelayArtifacts: 1, expectedHasQuorum: true},
		{name: "one of three", totalParticipants: 3, validRelayArtifacts: 1, expectedHasQuorum: false},
		{name: "two of three", totalParticipants: 3, validRelayArtifacts: 2, expectedHasQuorum: true},
		{name: "two of four", totalParticipants: 4, validRelayArtifacts: 2, expectedHasQuorum: false},
		{name: "three of four", totalParticipants: 4, validRelayArtifacts: 3, expectedHasQuorum: true},
	}

	for _, testCase := range testCases {
		actual := hasV2MissedInferenceQuorum(testCase.totalParticipants, testCase.validRelayArtifacts)
		if actual != testCase.expectedHasQuorum {
			t.Fatalf("%s: expected %v, got %v", testCase.name, testCase.expectedHasQuorum, actual)
		}
	}
}
