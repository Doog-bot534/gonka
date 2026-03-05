package keeper

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) CreateEscrow(goCtx context.Context, msg *types.MsgCreateEscrow) (*types.MsgCreateEscrowResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	modelID := msg.ModelId

	if modelID == "" {
		return nil, types.ErrEscrowModelIdEmpty
	}
	if !k.IsValidGovernanceModel(ctx, modelID) {
		return nil, types.ErrInvalidModel
	}

	escrowID, err := k.allocateNextEscrowID(ctx)
	if err != nil {
		return nil, err
	}

	exists, err := k.EscrowAccessByID.Has(ctx, escrowID)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, types.ErrEscrowAlreadyExists
	}

	if err := k.EscrowAccessByID.Set(ctx, escrowID, types.EscrowAccess{
		DeveloperAddress: msg.Creator,
		ModelId:          modelID,
	}); err != nil {
		return nil, err
	}
	developerPubKey := k.resolveEscrowEventDeveloperPubKey(ctx, msg.Creator)
	escrowEpochID := k.resolveEscrowEventEpochID(ctx)

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			"escrow_created",
			sdk.NewAttribute("developer_address", msg.Creator),
			sdk.NewAttribute("developer_pubkey", developerPubKey),
			sdk.NewAttribute("escrow_id", escrowID),
			sdk.NewAttribute("model_id", modelID),
			sdk.NewAttribute("epoch_id", escrowEpochID),
		),
	)

	k.LogInfo("Created escrow access record", types.Inferences,
		"developer", msg.Creator,
		"escrow_id", escrowID,
		"model_id", modelID,
	)

	return &types.MsgCreateEscrowResponse{EscrowId: escrowID}, nil
}

func (k msgServer) allocateNextEscrowID(ctx sdk.Context) (string, error) {
	current, err := k.EscrowGlobalSequence.Get(ctx)
	if err != nil {
		if !errors.Is(err, collections.ErrNotFound) {
			return "", err
		}
		current = 0
	}

	next := current + 1
	if err := k.EscrowGlobalSequence.Set(ctx, next); err != nil {
		return "", err
	}

	return strconv.FormatUint(next, 10), nil
}

func (k msgServer) resolveEscrowEventDeveloperPubKey(ctx sdk.Context, address string) string {
	// Msg-server unit tests call handlers directly without tx bytes/signatures.
	// Skip account pubkey resolution in that context to avoid unnecessary keeper reads.
	if len(ctx.TxBytes()) == 0 {
		return ""
	}
	accAddress, err := sdk.AccAddressFromBech32(address)
	if err != nil {
		return ""
	}
	account := k.AccountKeeper.GetAccount(ctx, accAddress)
	if account == nil || account.GetPubKey() == nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(account.GetPubKey().Bytes())
}

func (k msgServer) resolveEscrowEventEpochID(ctx sdk.Context) string {
	epochID, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		return ""
	}
	return strconv.FormatUint(epochID, 10)
}
