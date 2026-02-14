package types

// This file defines GetSigners() implementations for messages to satisfy the
// HasSigners interface expected by keeper.CheckPermission.

// Governance-authority messages
func (msg *MsgUpdateParams) GetSigners() []string                    { return []string{msg.Authority} }
func (msg *MsgRegisterModel) GetSigners() []string                   { return []string{msg.Authority} }
func (msg *MsgApproveBridgeTokenForTrading) GetSigners() []string    { return []string{msg.Authority} }
func (msg *MsgRegisterBridgeAddresses) GetSigners() []string         { return []string{msg.Authority} }
func (msg *MsgRegisterLiquidityPool) GetSigners() []string           { return []string{msg.Authority} }
func (msg *MsgRegisterTokenMetadata) GetSigners() []string           { return []string{msg.Authority} }
func (msg *MsgMigrateAllWrappedTokens) GetSigners() []string         { return []string{msg.Authority} }
func (msg *MsgRegisterWrappedTokenContract) GetSigners() []string    { return []string{msg.Authority} }
func (msg *MsgAddUserToTrainingAllowList) GetSigners() []string      { return []string{msg.Authority} }
func (msg *MsgCreatePartialUpgrade) GetSigners() []string            { return []string{msg.Authority} }
func (msg *MsgRemoveUserFromTrainingAllowList) GetSigners() []string { return []string{msg.Authority} }
func (msg *MsgSetTrainingAllowList) GetSigners() []string            { return []string{msg.Authority} }

// Creator signed messages
func (msg *MsgCreateTrainingTask) GetSigners() []string               { return []string{msg.Creator} }
func (msg *MsgCreateDummyTrainingTask) GetSigners() []string          { return []string{msg.Creator} }
func (msg *MsgAssignTrainingTask) GetSigners() []string               { return []string{msg.Creator} }
func (msg *MsgClaimTrainingTaskForAssignment) GetSigners() []string   { return []string{msg.Creator} }
func (msg *MsgFinishInference) GetSigners() []string                  { return []string{msg.Creator} }
func (msg *MsgInvalidateInference) GetSigners() []string              { return []string{msg.Creator} }
func (msg *MsgRevalidateInference) GetSigners() []string              { return []string{msg.Creator} }
func (msg *MsgStartInference) GetSigners() []string                   { return []string{msg.Creator} }
func (msg *MsgJoinTraining) GetSigners() []string                     { return []string{msg.Creator} }
func (msg *MsgJoinTrainingStatus) GetSigners() []string               { return []string{msg.Creator} }
func (msg *MsgSetBarrier) GetSigners() []string                       { return []string{msg.Creator} }
func (msg *MsgSubmitHardwareDiff) GetSigners() []string               { return []string{msg.Creator} }
func (msg *MsgSubmitNewParticipant) GetSigners() []string             { return []string{msg.Creator} }
func (msg *MsgSubmitNewUnfundedParticipant) GetSigners() []string     { return []string{msg.Creator} }
func (msg *MsgSubmitPocBatch) GetSigners() []string                   { return []string{msg.Creator} }
func (msg *MsgSubmitPocValidation) GetSigners() []string              { return []string{msg.Creator} }
func (msg *MsgSubmitSeed) GetSigners() []string                       { return []string{msg.Creator} }
func (msg *MsgSubmitTrainingKvRecord) GetSigners() []string           { return []string{msg.Creator} }
func (msg *MsgSubmitUnitOfComputePriceProposal) GetSigners() []string { return []string{msg.Creator} }
func (msg *MsgTrainingHeartbeat) GetSigners() []string                { return []string{msg.Creator} }
func (msg *MsgValidation) GetSigners() []string                       { return []string{msg.Creator} }
func (msg *MsgClaimRewards) GetSigners() []string                     { return []string{msg.Creator} }
func (msg *MsgRequestBridgeMint) GetSigners() []string                { return []string{msg.Creator} }
func (msg *MsgRequestBridgeWithdrawal) GetSigners() []string          { return []string{msg.Creator} }

// And one validator signed message?
func (msg *MsgBridgeExchange) GetSigners() []string { return []string{msg.Validator} }
