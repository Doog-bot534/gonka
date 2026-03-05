package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgCreateEscrow{}

func NewMsgCreateEscrow(creator, modelID string) *MsgCreateEscrow {
	return &MsgCreateEscrow{
		Creator: creator,
		ModelId: modelID,
	}
}

func (msg *MsgCreateEscrow) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	if msg.ModelId == "" {
		return ErrEscrowModelIdEmpty
	}
	return nil
}
