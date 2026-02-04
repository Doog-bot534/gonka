package epochgroup

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"strconv"

	"github.com/cometbft/cometbft/libs/bytes"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetRandomMemberForModel gets a random member for a specific model
func (eg *EpochGroup) GetRandomMemberForModel(
	goCtx context.Context,
	modelId string,
	filterFn func([]*group.GroupMember) []*group.GroupMember,
) (*types.Participant, error) {
	// If modelId is provided and this is the parent group, delegate to the sub-group
	if modelId != "" && eg.GroupData.GetModelId() == "" {
		subGroup, err := eg.GetSubGroup(goCtx, modelId)
		if err != nil {
			return nil, status.Error(codes.Internal, fmt.Sprintf("Error getting sub-group for model %s: %v", modelId, err))
		}
		return subGroup.GetRandomMember(goCtx, filterFn)
	}

	// Otherwise, get a random member from this group
	return eg.GetRandomMember(goCtx, filterFn)
}

func (eg *EpochGroup) GetRandomMember(
	goCtx context.Context,
	filterFn func([]*group.GroupMember) []*group.GroupMember,
) (*types.Participant, error) {
	// Use the context as is, don't try to unwrap it
	// This allows the method to work with both SDK contexts and regular contexts
	ctx := goCtx

	activeParticipants, err := eg.getAllGroupMembersPaginated(ctx, uint64(eg.GroupData.EpochGroupId))
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if len(activeParticipants) == 0 {
		return nil, status.Error(codes.Internal, "Active participants found, but length is 0")
	}

	filteredParticipants := filterFn(activeParticipants)
	if len(filteredParticipants) == 0 {
		return nil, status.Error(codes.Internal, "After filtering participants the length is 0")
	}

	participantIndex := selectRandomParticipant(filteredParticipants)

	participant, ok := eg.ParticipantKeeper.GetParticipant(ctx, participantIndex)
	if !ok {
		msg := fmt.Sprintf(
			"Selected active participant, but not found in participants list. index = %s", participantIndex,
		)
		return nil, status.Error(codes.Internal, msg)
	}
	return &participant, nil
}

func selectRandomParticipant(participants []*group.GroupMember) string {
	cumulativeArray := computeCumulativeArray(participants)

	randomNumber := rand.Int63n(cumulativeArray[len(cumulativeArray)-1])
	for i, cumulativeWeight := range cumulativeArray {
		if randomNumber < cumulativeWeight {
			return participants[i].Member.Address
		}
	}

	return participants[len(participants)-1].Member.Address
}

func computeCumulativeArray(participants []*group.GroupMember) []int64 {
	cumulativeArray := make([]int64, len(participants))
	cumulativeArray[0] = int64(getWeight(participants[0]))
	for i := 1; i < len(participants); i++ {
		cumulativeArray[i] = cumulativeArray[i-1] + getWeight(participants[i])
	}
	return cumulativeArray
}

func getWeight(participant *group.GroupMember) int64 {
	weight, err := strconv.Atoi(participant.Member.Weight)
	if err != nil {
		return 0
	}
	return int64(weight)
}

type ReplayableRandomContext struct {
	Participants []*group.GroupMember
	Seed [32]byte
	SeenIndices map[int]bool
	CumulativeArray []int64
}

func InitialSeed(blockHash bytes.HexBytes) [32]byte {
	bytes := blockHash
	seed := sha256.Sum256(bytes)
	return seed
}

func (eg *EpochGroup) NewReplayableRandomContext(
	ctx context.Context,
	blockHash bytes.HexBytes,
) (*ReplayableRandomContext, error) {
	blockHash = blockHash.Bytes()

	participants, err := eg.getAllGroupMembersPaginated(ctx, uint64(eg.GroupData.EpochGroupId))
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if len(participants) == 0 {
		return nil, status.Error(codes.Internal, "Active participants found, but length is 0")
	}

	seed := InitialSeed(blockHash)

	cumulativeArray := computeCumulativeArray(participants)
	seenIndices := map[int]bool{}

	replayableRandomCtx := &ReplayableRandomContext{
		participants,
		seed,
		seenIndices,
		cumulativeArray,
	}
	return replayableRandomCtx, nil
}

func (eg *EpochGroup) MakeRandomMemberReplayableFn(
	goCtx context.Context,
	blockHash bytes.HexBytes,
) func() (*types.Participant, error) {
	replayableRandomCtx, err := eg.NewReplayableRandomContext(goCtx, blockHash)
	if err != nil {
		return func() (*types.Participant, error) { return nil, err }
	}
	return func() (*types.Participant, error) {
		return eg.GetRandomMemberReplayable(goCtx, replayableRandomCtx)
	}
}

func (eg *EpochGroup) GetRandomMemberReplayable(
	goCtx context.Context,
	replayableRandomCtx *ReplayableRandomContext,
) (*types.Participant, error) {
	if len(replayableRandomCtx.Participants) == 0 {
		return nil, status.Error(codes.Internal, "Active participants found, but length is 0")
	}

	participantAddress, err := selectRandomParticipantReplayable(replayableRandomCtx)
	if err != nil {
		return nil, err
	}

	participant, ok := eg.ParticipantKeeper.GetParticipant(goCtx, participantAddress)
	if !ok {
		msg := fmt.Sprintf(
			"Selected active participant, but not found in participants list. index = %s", participantAddress,
		)
		return nil, status.Error(codes.Internal, msg)
	}
	return &participant, nil
}

func selectRandomParticipantReplayable(ctx *ReplayableRandomContext) (string, error) {
	participantsCnt := len(ctx.Participants)
	if len(ctx.SeenIndices) >= participantsCnt {
		return "", status.Error(codes.Internal, "No participants to sample")
	}

	weightSum := ctx.CumulativeArray[participantsCnt-1]
	for {
		currentSeed := ctx.Seed[:]
		randomWeight := int64(binary.LittleEndian.Uint64(currentSeed)) % weightSum

		index := upperBound(randomWeight, ctx.CumulativeArray)
		if index >= participantsCnt {
			index = participantsCnt-1
		}

		ctx.Seed = sha256.Sum256(currentSeed)
		if !ctx.SeenIndices[index] {
			ctx.SeenIndices[index] = true
			return ctx.Participants[index].Member.Address, nil
		}
	}
}

// Performs a binary search, searching for the lowest value greater than the needle in the haystack.
// Assumes the input array is already sorted.
func upperBound[T cmp.Ordered](needle T, haystack []T) int {
	low, high := 0, len(haystack)
	for low < high {
		middle := low + (high - low) / 2
		if needle < haystack[middle] {
			high = middle
		} else {
			low = middle + 1
		}
	}
	return low
}
