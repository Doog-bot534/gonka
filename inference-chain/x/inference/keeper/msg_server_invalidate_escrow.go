package keeper

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const v2DeveloperBlockSignDomain = "v2_dev_block_sig_v1"

func (k msgServer) InvalidateEscrow(goCtx context.Context, msg *types.MsgInvalidateEscrow) (*types.MsgInvalidateEscrowResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	access, err := k.EscrowAccessByID.Get(ctx, msg.EscrowId)
	if err != nil {
		return nil, types.ErrEscrowNotFound
	}
	if access.DeveloperAddress != msg.DeveloperAddress {
		return nil, types.ErrEscrowConflictEvidenceInvalid
	}

	alreadyInvalidated, err := k.EscrowInvalidatedByID.Has(ctx, msg.EscrowId)
	if err != nil {
		return nil, err
	}
	if alreadyInvalidated {
		return &types.MsgInvalidateEscrowResponse{}, nil
	}

	leftBlockHash := msg.LeftBlockHash
	rightBlockHash := msg.RightBlockHash
	if containsWhitespace(leftBlockHash) || containsWhitespace(rightBlockHash) {
		return nil, types.ErrEscrowConflictEvidenceInvalid
	}
	if leftBlockHash == rightBlockHash {
		return nil, types.ErrEscrowConflictEvidenceInvalid
	}
	leftBlockMessagesHash, err := parseHex32(msg.LeftBlockMessagesHash)
	if err != nil {
		return nil, types.ErrEscrowConflictEvidenceInvalid
	}
	rightBlockMessagesHash, err := parseHex32(msg.RightBlockMessagesHash)
	if err != nil {
		return nil, types.ErrEscrowConflictEvidenceInvalid
	}
	if leftBlockMessagesHash == rightBlockMessagesHash {
		return nil, types.ErrEscrowConflictEvidenceInvalid
	}
	leftExpectedHash := computeV2ConflictBlockHash(
		ctx.ChainID(),
		msg.EscrowId,
		msg.BlockSequence,
		leftBlockMessagesHash,
	)
	rightExpectedHash := computeV2ConflictBlockHash(
		ctx.ChainID(),
		msg.EscrowId,
		msg.BlockSequence,
		rightBlockMessagesHash,
	)
	if leftExpectedHash != leftBlockHash || rightExpectedHash != rightBlockHash {
		return nil, types.ErrEscrowConflictEvidenceInvalid
	}

	developerAccAddress, err := sdk.AccAddressFromBech32(access.DeveloperAddress)
	if err != nil {
		return nil, types.ErrEscrowConflictEvidenceInvalid
	}
	developerAccount := k.AccountKeeper.GetAccount(ctx, developerAccAddress)
	if developerAccount == nil || developerAccount.GetPubKey() == nil {
		return nil, types.ErrEscrowConflictEvidenceInvalid
	}
	if containsWhitespace(msg.LeftBlockSignature) || containsWhitespace(msg.RightBlockSignature) {
		return nil, types.ErrEscrowConflictEvidenceInvalid
	}
	leftSignature, err := base64.StdEncoding.DecodeString(msg.LeftBlockSignature)
	if err != nil {
		return nil, types.ErrEscrowConflictEvidenceInvalid
	}
	rightSignature, err := base64.StdEncoding.DecodeString(msg.RightBlockSignature)
	if err != nil {
		return nil, types.ErrEscrowConflictEvidenceInvalid
	}
	// Developer block signatures are over the ascii hex hash payload.
	if !developerAccount.GetPubKey().VerifySignature([]byte(leftExpectedHash), leftSignature) {
		return nil, types.ErrEscrowConflictEvidenceInvalid
	}
	if !developerAccount.GetPubKey().VerifySignature([]byte(rightExpectedHash), rightSignature) {
		return nil, types.ErrEscrowConflictEvidenceInvalid
	}

	if err := k.EscrowInvalidatedByID.Set(ctx, msg.EscrowId); err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			"escrow_invalidated",
			sdk.NewAttribute("escrow_id", msg.EscrowId),
			sdk.NewAttribute("developer_address", msg.DeveloperAddress),
			sdk.NewAttribute("block_sequence", strconv.FormatUint(msg.BlockSequence, 10)),
			sdk.NewAttribute("left_block_hash", msg.LeftBlockHash),
			sdk.NewAttribute("right_block_hash", msg.RightBlockHash),
			sdk.NewAttribute("left_block_signature", msg.LeftBlockSignature),
			sdk.NewAttribute("right_block_signature", msg.RightBlockSignature),
			sdk.NewAttribute("left_block_messages_hash", msg.LeftBlockMessagesHash),
			sdk.NewAttribute("right_block_messages_hash", msg.RightBlockMessagesHash),
		),
	)

	return &types.MsgInvalidateEscrowResponse{}, nil
}

func parseHex32(value string) ([32]byte, error) {
	var out [32]byte
	if containsWhitespace(value) {
		return out, fmt.Errorf("unexpected whitespace")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return out, err
	}
	if len(decoded) != len(out) {
		return out, fmt.Errorf("invalid hash length")
	}
	copy(out[:], decoded)
	return out, nil
}

func computeV2ConflictBlockHash(
	chainID string,
	escrowID string,
	blockSequence uint64,
	blockMessagesHash [32]byte,
) string {
	preimage := buildV2ConflictBlockSigningPreimage(chainID, escrowID, blockSequence, blockMessagesHash)
	hash := sha256.Sum256(preimage)
	return fmt.Sprintf("%x", hash[:])
}

func buildV2ConflictBlockSigningPreimage(
	chainID string,
	escrowID string,
	blockSequence uint64,
	blockMessagesHash [32]byte,
) []byte {
	var buffer bytes.Buffer
	writeLengthPrefixedString(&buffer, v2DeveloperBlockSignDomain)
	writeLengthPrefixedString(&buffer, chainID)
	writeLengthPrefixedString(&buffer, escrowID)
	writeUint64(&buffer, blockSequence)
	_, _ = buffer.Write(blockMessagesHash[:])
	return buffer.Bytes()
}

func writeLengthPrefixedString(buffer *bytes.Buffer, value string) {
	var lengthBytes [4]byte
	binary.BigEndian.PutUint32(lengthBytes[:], uint32(len(value)))
	_, _ = buffer.Write(lengthBytes[:])
	_, _ = buffer.WriteString(value)
}

func writeUint64(buffer *bytes.Buffer, value uint64) {
	var valueBytes [8]byte
	binary.BigEndian.PutUint64(valueBytes[:], value)
	_, _ = buffer.Write(valueBytes[:])
}

func containsWhitespace(value string) bool {
	return strings.IndexFunc(value, unicode.IsSpace) >= 0
}
