package keeper

import (
	"context"
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) RegisterIbcTokenMetadata(goCtx context.Context, msg *types.MsgRegisterIbcTokenMetadata) (*types.MsgRegisterIbcTokenMetadataResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	// Validate authority - only governance can set token metadata
	if msg.Authority != k.GetAuthority() {
		return nil, types.ErrInvalidSigner
	}

	// Create BridgeTokenMetadata struct from the message
	metadata := types.BridgeTokenMetadata{
		ChainId:         msg.ChainId,
		ContractAddress: msg.IbcDenom, // For IBC tokens, the "contract address" is the denom
		Name:            msg.Name,
		Symbol:          msg.Symbol,
		Decimals:        uint32(msg.Decimals),
	}

	// Set the IBC token metadata in our custom store
	err := k.SetIBCTokenMetadata(ctx, msg.ChainId, msg.IbcDenom, metadata)
	if err != nil {
		return nil, err
	}

	// Also update x/bank denom metadata so standard Cosmos tools and explorers
	// can query correct decimals/symbol via bank/v1beta1/denoms_metadata
	bankMetadata := banktypes.Metadata{
		Description: fmt.Sprintf("IBC token from %s, registered via governance", msg.ChainId),
		DenomUnits: []*banktypes.DenomUnit{
			{
				Denom:    msg.IbcDenom,
				Exponent: 0,
			},
			{
				Denom:    msg.Symbol,
				Exponent: uint32(msg.Decimals),
			},
		},
		Base:    msg.IbcDenom,
		Display: msg.Symbol,
		Name:    msg.Name,
		Symbol:  msg.Symbol,
	}
	k.BankView.SetDenomMetaData(ctx, bankMetadata)

	return &types.MsgRegisterIbcTokenMetadataResponse{}, nil
}
