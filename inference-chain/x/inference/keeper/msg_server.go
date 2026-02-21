package keeper

import (
	"github.com/productscience/inference/x/inference/types"
)

type msgServer struct {
	Keeper
	failOnCompareMismatch bool
}

// NewMsgServerImpl returns an implementation of the MsgServer interface
// for the provided Keeper.
func NewMsgServerImpl(keeper Keeper) types.MsgServer {
	return &msgServer{
		Keeper:                keeper,
		failOnCompareMismatch: true,
	}
}

// NewMsgServerImplWithCompareMismatchMode allows selecting whether compare mismatches
// should fail the message (true) or only be logged (false).
func NewMsgServerImplWithCompareMismatchMode(keeper Keeper, failOnCompareMismatch bool) types.MsgServer {
	return &msgServer{
		Keeper:                keeper,
		failOnCompareMismatch: failOnCompareMismatch,
	}
}

var _ types.MsgServer = msgServer{}
