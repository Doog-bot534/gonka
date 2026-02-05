package voting

import (
	"context"

	"encoding/binary"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "cosmossdk.io/errors"

	"github.com/cometbft/cometbft/libs/bytes"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/epochgroup"
	"github.com/productscience/inference/api/inference/inference"
)

const (
	ModuleName = "votes"
)

type VoteValidator struct {
	EpochGroup *epochgroup.EpochGroup
	Ctx context.Context
	BlockHash bytes.HexBytes
	Result VotingResult
	Config VotingConfig
	VotingRequest VotingInitiateRequest
	StartInferenceRequest inference.MsgStartInference
}

var errorRegisterCounter uint32 = 1100
func registerError(description string) error {
	err := sdkerrors.Register(ModuleName, errorRegisterCounter, description)
	errorRegisterCounter++
	return err
}

// TODO: Inferencetypes has error codes used for validations, should we use those instead?
// NOTE: These errors are sorted alphabetically.
var (
	ErrAssigneeMismatch             = registerError("assignee does not match challenger")
	ErrCreatorMismatch              = registerError("creator does not match challenger")
	ErrDuplicatePositiveVote        = registerError("more than one positive vote was collected")
	ErrExceededMaximumNegativeVotes = registerError("too many negative votes were collected")
	ErrInferenceIdMismatch          = registerError("inference IDs do not match")
	ErrInvalidChallengerRole        = registerError("invalid challenger role")
	ErrInvalidNegativeOutcome       = registerError("negative outcome with invalid number of votes")
	ErrInvalidVoteType              = registerError("invalid vote type")
	ErrNegativeVoteAfterPositive    = registerError("a negative vote was collected after a positive vote")
	ErrSeedMismatch                 = registerError("initial seeds do not match")
	ErrTooManySkips                 = registerError("too many voters were skipped")
)

func ValidateVote(vv *VoteValidator) error {
	randomCtx, err := vv.EpochGroup.NewReplayableRandomContext(vv.Ctx, vv.BlockHash)
	if err != nil {
		return err // TODO: What to do here?
	}

	if randomCtx.Seed != vv.VotingRequest.AssignmentProof.Seed {
		return ErrSeedMismatch
	}

	if vv.Result.InferenceId != vv.VotingRequest.InferenceId || vv.Result.InferenceId != vv.StartInferenceRequest.InferenceId {
		return ErrInferenceIdMismatch
	}

	switch vv.VotingRequest.ChallengerRole {
	case RoleExecutor:
		if vv.StartInferenceRequest.AssignedTo != vv.VotingRequest.ChallengerAddress {
			return ErrAssigneeMismatch
		}
	case RoleTA:
		if vv.StartInferenceRequest.Creator != vv.VotingRequest.ChallengerAddress {
			return ErrCreatorMismatch
		}
	default:
		return ErrInvalidChallengerRole
	}

	// Validate challenger signature
	message := []byte{}
	message = append(message, vv.VotingRequest.InferenceId...)
	message = append(message, vv.VotingRequest.ChallengerAddress...)
	message = append(message, vv.VotingRequest.RespondentAddress...)
	binary.LittleEndian.AppendUint64(message, uint64(vv.VotingRequest.Timestamp))
	if err := calculations.ValidateSignatureBytes(message, vv.VotingRequest.ChallengerAddress, vv.VotingRequest.ChallengerSignature); err != nil {
		return err
	}

	sdkCtx := sdk.UnwrapSDKContext(vv.Ctx)

	// TODO: Validate upper bound
	// TODO: Check that hashes match
	// TODO: Check auth key
	skippedMembersCounter := uint8(0)
	negativeVotesCounter := 0
	hasPositiveVote := false
	voteIdx := 0
	for voteIdx < len(vv.Result.Votes) {
		participant, err := vv.EpochGroup.GetRandomMemberReplayable(vv.Ctx, randomCtx)
		if err != nil {
			return err // TODO: What to do here?
		}

		vote := vv.Result.Votes[voteIdx]
		if vote.VoterAddress != participant.Address {
			skippedMembersCounter++
			if skippedMembersCounter > vv.Config.MaxNumSkips {
				return ErrTooManySkips
			}

			continue
		}

		if vote.InferenceId != vv.VotingRequest.InferenceId {
			return ErrInferenceIdMismatch
		}

		// Validate voter signature
		message := []byte{}
		message = append(message, vote.InferenceId...)
		message = append(message, vote.VoterAddress...)
		binary.LittleEndian.AppendUint16(message, uint16(vote.VoteType))
		message = append(message, vote.RespondentDataHash...)
		binary.LittleEndian.AppendUint64(message, uint64(vote.Timestamp))
		if err := calculations.ValidateSignatureBytes(message, vote.VoterAddress, vote.VoterSignature); err != nil {
			return err
		}

		calculations.ValidateTimestamp(
			vote.Timestamp,
			sdkCtx.BlockTime().UnixNano(),
			-1, // TODO: params.ValidationParams.TimestampExpiration,
			-1, // TODO: params.ValidationParams.TimestampAdvance,
			60 * int64(time.Second),
		)

		switch vote.VoteType {
		case VotePositive:
			if hasPositiveVote {
				return ErrDuplicatePositiveVote
			}
			hasPositiveVote = true
		case VoteNegative:
			negativeVotesCounter++
			if hasPositiveVote {
				return ErrNegativeVoteAfterPositive
			}
			if negativeVotesCounter > vv.Config.MaxNumNodes {
				return ErrExceededMaximumNegativeVotes
			}
		case VoteInvalid:
			return ErrInvalidVoteType
		default:
			return ErrInvalidVoteType
		}

		voteIdx++
	}

	if !hasPositiveVote && negativeVotesCounter != vv.Config.MaxNumNodes {
		return ErrInvalidNegativeOutcome
	}

	return nil
}
