package public

import (
	"context"
	"crypto/sha256"
	"decentralized-api/logging"
	"encoding/binary"
	"fmt"
	"net/http"
	"sort"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
)

type v2ParticipantSelectorFunc func(ctx context.Context, model string, escrowID string, sequence uint64) ([]string, error)
type v2ParticipantAddressResolverFunc func() string

type weightedParticipant struct {
	address string
	weight  uint64
}

func (s *Server) resolveV2ResponsibleParticipants(ctx context.Context, model string, escrowID string, sequence uint64) ([]string, error) {
	responsibleWeightedParticipants, err := s.resolveV2ResponsibleWeightedParticipants(ctx, model, escrowID, sequence)
	if err != nil {
		return nil, err
	}
	responsibleParticipants := make([]string, 0, len(responsibleWeightedParticipants))
	for _, participant := range responsibleWeightedParticipants {
		responsibleParticipants = append(responsibleParticipants, participant.address)
	}
	return responsibleParticipants, nil
}

func (s *Server) resolveV2ResponsibleWeightedParticipants(ctx context.Context, model string, escrowID string, sequence uint64) ([]weightedParticipant, error) {
	selectionCount := resolveV2ResponsibleParticipantCount(s.resolveCachedV2ResponsibleParticipantCount())
	epochID, ok := ctx.Value(v2EpochIDContextKey).(uint64)
	if !ok || epochID == 0 {
		return nil, ErrV2EscrowEpochUnavailable
	}
	if s.epochGroupDataCache == nil {
		return nil, echo.NewHTTPError(http.StatusServiceUnavailable, "Epoch cache unavailable")
	}

	modelEpochData, err := s.epochGroupDataCache.GetModelEpochGroupData(ctx, epochID, model)
	if err != nil {
		logging.Error("Failed to load model epoch data for v2 participant selection", types.Inferences,
			"model", model,
			"epoch", epochID,
			"error", err,
		)
		return nil, echo.NewHTTPError(http.StatusBadGateway, "Failed to load model participants")
	}

	weightedParticipants := make([]weightedParticipant, 0, len(modelEpochData.ValidationWeights))
	for _, weight := range modelEpochData.ValidationWeights {
		if weight.MemberAddress == "" || weight.Weight <= 0 {
			continue
		}
		weightedParticipants = append(weightedParticipants, weightedParticipant{
			address: weight.MemberAddress,
			weight:  uint64(weight.Weight),
		})
	}

	responsibleParticipants, err := selectResponsibleParticipantsDeterministic(weightedParticipants, selectionCount, escrowID, sequence)
	if err != nil {
		logging.Warn("Unable to select deterministic v2 participants", types.Inferences,
			"model", model,
			"escrow_id", escrowID,
			"sequence", sequence,
			"error", err,
		)
		return nil, echo.NewHTTPError(http.StatusServiceUnavailable, "No eligible participants for this model")
	}
	weightByAddress := make(map[string]uint64, len(weightedParticipants))
	for _, participant := range weightedParticipants {
		weightByAddress[participant.address] = participant.weight
	}
	responsibleWeightedParticipants := make([]weightedParticipant, 0, len(responsibleParticipants))
	for _, address := range responsibleParticipants {
		weight := weightByAddress[address]
		if weight == 0 {
			continue
		}
		responsibleWeightedParticipants = append(responsibleWeightedParticipants, weightedParticipant{
			address: address,
			weight:  weight,
		})
	}

	logging.Info("Resolved deterministic v2 participants", types.Inferences,
		"model", model,
		"escrow_id", escrowID,
		"sequence", sequence,
		"selection_count", selectionCount,
		"responsible_participants", responsibleParticipants,
	)
	return responsibleWeightedParticipants, nil
}

func resolveV2ResponsibleParticipantCount(count uint32) int {
	if count == 0 {
		return int(types.DefaultV2ResponsibleParticipantsCount)
	}
	return int(count)
}

func (s *Server) resolveCachedV2ResponsibleParticipantCount() uint32 {
	if s.phaseTracker == nil {
		return types.DefaultV2ResponsibleParticipantsCount
	}
	return s.phaseTracker.GetV2ResponsibleParticipantsCount()
}

func selectResponsibleParticipantsDeterministic(participants []weightedParticipant, selectionCount int, escrowID string, sequence uint64) ([]string, error) {
	if selectionCount <= 0 {
		return nil, fmt.Errorf("selection count must be greater than zero")
	}

	eligibleParticipants := make([]weightedParticipant, 0, len(participants))
	for _, participant := range participants {
		if participant.address == "" || participant.weight == 0 {
			continue
		}
		eligibleParticipants = append(eligibleParticipants, participant)
	}
	if len(eligibleParticipants) == 0 {
		return nil, fmt.Errorf("no eligible participants")
	}

	sort.Slice(eligibleParticipants, func(i, j int) bool {
		return eligibleParticipants[i].address < eligibleParticipants[j].address
	})

	if selectionCount > len(eligibleParticipants) {
		selectionCount = len(eligibleParticipants)
	}

	seedHash := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", escrowID, sequence)))
	responsibleParticipants := make([]string, 0, selectionCount)

	for drawIndex := 0; drawIndex < selectionCount; drawIndex++ {
		totalWeight := uint64(0)
		for _, participant := range eligibleParticipants {
			totalWeight += participant.weight
		}
		if totalWeight == 0 {
			return nil, fmt.Errorf("eligible participants have zero total weight")
		}

		ticket := deterministicWeightTicket(seedHash, uint64(drawIndex)) % totalWeight

		cumulativeWeight := uint64(0)
		responsibleIndex := len(eligibleParticipants) - 1
		for idx, participant := range eligibleParticipants {
			cumulativeWeight += participant.weight
			if ticket < cumulativeWeight {
				responsibleIndex = idx
				break
			}
		}

		responsibleParticipants = append(responsibleParticipants, eligibleParticipants[responsibleIndex].address)
		eligibleParticipants = append(eligibleParticipants[:responsibleIndex], eligibleParticipants[responsibleIndex+1:]...)
	}

	return responsibleParticipants, nil
}

func deterministicWeightTicket(seedHash [32]byte, drawIndex uint64) uint64 {
	var drawInput [40]byte
	copy(drawInput[:32], seedHash[:])
	binary.BigEndian.PutUint64(drawInput[32:], drawIndex)

	drawHash := sha256.Sum256(drawInput[:])
	return binary.BigEndian.Uint64(drawHash[:8])
}

func (s *Server) resolveV2LocalParticipantAddress() string {
	if s.nodeBroker == nil {
		return ""
	}
	return s.nodeBroker.GetParticipantAddress()
}

func isResponsibleParticipant(responsibleParticipants []string, participantAddress string) bool {
	for _, responsibleParticipant := range responsibleParticipants {
		if responsibleParticipant == participantAddress {
			return true
		}
	}
	return false
}
