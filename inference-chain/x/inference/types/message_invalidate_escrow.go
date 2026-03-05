package types

import (
	"strings"
	"unicode"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgInvalidateEscrow{}

func NewMsgInvalidateEscrow(
	creator string,
	escrowID string,
	developerAddress string,
	blockSequence uint64,
	leftBlockHash string,
	rightBlockHash string,
	leftBlockSignature string,
	rightBlockSignature string,
	leftBlockMessagesHash string,
	rightBlockMessagesHash string,
) *MsgInvalidateEscrow {
	return &MsgInvalidateEscrow{
		Creator:                creator,
		EscrowId:               escrowID,
		DeveloperAddress:       developerAddress,
		BlockSequence:          blockSequence,
		LeftBlockHash:          leftBlockHash,
		RightBlockHash:         rightBlockHash,
		LeftBlockSignature:     leftBlockSignature,
		RightBlockSignature:    rightBlockSignature,
		LeftBlockMessagesHash:  leftBlockMessagesHash,
		RightBlockMessagesHash: rightBlockMessagesHash,
	}
}

func (msg *MsgInvalidateEscrow) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	if msg.EscrowId == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "escrow_id is required")
	}
	if msg.DeveloperAddress == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "developer_address is required")
	}
	if msg.BlockSequence == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "block_sequence must be greater than zero")
	}
	leftHash := msg.LeftBlockHash
	rightHash := msg.RightBlockHash
	if leftHash == "" || rightHash == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "left_block_hash and right_block_hash are required")
	}
	if containsWhitespace(leftHash) || containsWhitespace(rightHash) {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "left_block_hash and right_block_hash must be canonical without whitespace")
	}
	if leftHash == rightHash {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "left_block_hash and right_block_hash must differ")
	}
	leftSignature := msg.LeftBlockSignature
	rightSignature := msg.RightBlockSignature
	if leftSignature == "" || rightSignature == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "left_block_signature and right_block_signature are required")
	}
	if containsWhitespace(leftSignature) || containsWhitespace(rightSignature) {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "left_block_signature and right_block_signature must be canonical without whitespace")
	}
	leftMessagesHash := msg.LeftBlockMessagesHash
	rightMessagesHash := msg.RightBlockMessagesHash
	if leftMessagesHash == "" || rightMessagesHash == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "left_block_messages_hash and right_block_messages_hash are required")
	}
	if containsWhitespace(leftMessagesHash) || containsWhitespace(rightMessagesHash) {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "left_block_messages_hash and right_block_messages_hash must be canonical without whitespace")
	}
	if leftMessagesHash == rightMessagesHash {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "left_block_messages_hash and right_block_messages_hash must differ")
	}
	return nil
}

func containsWhitespace(value string) bool {
	return strings.IndexFunc(value, unicode.IsSpace) >= 0
}
