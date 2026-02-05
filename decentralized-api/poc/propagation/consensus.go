package propagation

import (
	"sort"
)

type ConsensusResult struct {
	PocHeight       int64  `json:"poc_height"`
	Participant     string `json:"participant"`
	AgreedCount     int64  `json:"agreed_count"`
	TotalValidators int    `json:"total_validators"`
	AgreeingCount   int    `json:"agreeing_count"`
}

func CalculateConsensus(
	observations []FirstArrivalObservation,
	bundles []BundleHeader,
	deadline int64,
) map[string]ConsensusResult {
	if len(observations) == 0 {
		return make(map[string]ConsensusResult)
	}

	totalValidators := len(observations)
	requiredAgreement := totalValidators/2 + 1

	bundlesByParticipant := make(map[string][]BundleHeader)
	for _, b := range bundles {
		bundlesByParticipant[b.Participant] = append(bundlesByParticipant[b.Participant], b)
	}

	for participant := range bundlesByParticipant {
		sort.Slice(bundlesByParticipant[participant], func(i, j int) bool {
			return bundlesByParticipant[participant][i].Count < bundlesByParticipant[participant][j].Count
		})
	}

	results := make(map[string]ConsensusResult)

	for participant, headers := range bundlesByParticipant {
		var agreedCount int64 = 0
		var agreeingCount int = 0

		for _, header := range headers {
			targetCount := uint32(header.Count)
			validatorsInTime := 0

			for _, obs := range observations {
				arrival, ok := obs.Arrivals[participant]
				if ok && arrival.Time <= deadline && arrival.Count >= targetCount {
					validatorsInTime++
				}
			}

			if validatorsInTime >= requiredAgreement {
				agreedCount = int64(targetCount)
				agreeingCount = validatorsInTime
			}
		}

		results[participant] = ConsensusResult{
			PocHeight:       headers[0].PocHeight,
			Participant:     participant,
			AgreedCount:     agreedCount,
			TotalValidators: totalValidators,
			AgreeingCount:   agreeingCount,
		}
	}

	return results
}

type ConsensusCalculator struct {
	cache *Cache
}

func NewConsensusCalculator(cache *Cache) *ConsensusCalculator {
	return &ConsensusCalculator{
		cache: cache,
	}
}

func (c *ConsensusCalculator) Calculate(pocHeight int64, deadline int64) (map[string]ConsensusResult, error) {
	observations, err := c.cache.GetObservations(pocHeight)
	if err != nil {
		return nil, err
	}

	bundles := c.cache.AllBundlesForHeight(pocHeight)

	return CalculateConsensus(observations, bundles, deadline), nil
}

func (c *ConsensusCalculator) CalculateForParticipant(pocHeight int64, participant string, deadline int64) (*ConsensusResult, error) {
	results, err := c.Calculate(pocHeight, deadline)
	if err != nil {
		return nil, err
	}

	result, ok := results[participant]
	if !ok {
		return nil, nil
	}

	return &result, nil
}

func (c *ConsensusCalculator) CalculateForParticipantWithDeadlineFromObservations(pocHeight int64, participant string) (*ConsensusResult, error) {
	observations, err := c.cache.GetObservations(pocHeight)
	if err != nil {
		return nil, err
	}

	if len(observations) == 0 {
		return nil, nil
	}

	deadline := observations[0].Timestamp
	for _, obs := range observations[1:] {
		if obs.Timestamp < deadline {
			deadline = obs.Timestamp
		}
	}

	return c.CalculateForParticipant(pocHeight, participant, deadline)
}

