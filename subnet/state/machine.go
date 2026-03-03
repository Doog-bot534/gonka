package state

import (
	"fmt"
	"slices"

	"google.golang.org/protobuf/proto"

	"subnet/signing"
	"subnet/types"
)

// StateMachine applies diffs and tracks session state.
type StateMachine struct {
	state    *types.EscrowState
	verifier signing.Verifier

	// Lookup maps derived from group at construction time.
	slotToAddress map[uint32]string
	slotToPubKey  map[uint32][]byte
	addressToSlot map[string]uint32
	totalSlots    uint32
}

// NewStateMachine creates a state machine for a session.
func NewStateMachine(
	escrowID string,
	config types.SessionConfig,
	group []types.SlotAssignment,
	balance uint64,
	userAddress string,
	verifier signing.Verifier,
) *StateMachine {
	slotToAddr := make(map[uint32]string, len(group))
	slotToPub := make(map[uint32][]byte, len(group))
	addrToSlot := make(map[string]uint32, len(group))
	for _, s := range group {
		slotToAddr[s.SlotID] = s.ValidatorAddress
		slotToPub[s.SlotID] = s.PublicKey
		addrToSlot[s.ValidatorAddress] = s.SlotID
	}

	groupCopy := make([]types.SlotAssignment, len(group))
	copy(groupCopy, group)

	hostStats := make(map[uint32]*types.HostStats, len(group))
	for _, s := range group {
		hostStats[s.SlotID] = &types.HostStats{}
	}

	return &StateMachine{
		state: &types.EscrowState{
			EscrowID:   escrowID,
			Config:     config,
			Group:      groupCopy,
			Balance:    balance,
			Inferences: make(map[uint64]*types.InferenceRecord),
			HostStats:  hostStats,
		},
		verifier:      verifier,
		slotToAddress: slotToAddr,
		slotToPubKey:  slotToPub,
		addressToSlot: addrToSlot,
		totalSlots:    uint32(len(group)),
	}
}

// ApplyDiff validates and applies a diff, returning the state root.
func (sm *StateMachine) ApplyDiff(diff types.Diff, userAddress string) ([]byte, error) {
	// 1. Verify user signature.
	diffContent := sm.buildDiffContent(diff)
	data, err := proto.Marshal(diffContent)
	if err != nil {
		return nil, fmt.Errorf("marshal diff content: %w", err)
	}

	recovered, err := sm.verifier.RecoverAddress(data, diff.UserSig)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", types.ErrInvalidUserSig, err)
	}
	if recovered != userAddress {
		return nil, fmt.Errorf("%w: expected %s, got %s", types.ErrInvalidUserSig, userAddress, recovered)
	}

	// 2. Validate nonce.
	expectedNonce := sm.state.LatestNonce + 1
	if diff.Nonce != expectedNonce {
		return nil, fmt.Errorf("%w: expected %d, got %d", types.ErrInvalidNonce, expectedNonce, diff.Nonce)
	}

	// 3. Validate at most one MsgStartInference per diff.
	startCount := 0
	for _, tx := range diff.Txs {
		if tx.GetStartInference() != nil {
			startCount++
		}
	}
	if startCount > 1 {
		return nil, types.ErrMultipleStartMsgs
	}

	// 4. Apply each tx.
	for _, tx := range diff.Txs {
		if err := sm.applyTx(tx); err != nil {
			return nil, err
		}
	}

	// 5. Update nonce.
	sm.state.LatestNonce = diff.Nonce

	// 6. Compute state root.
	root := ComputeStateRoot(sm.state.Balance, sm.state.HostStats, sm.state.Inferences)
	return root, nil
}

// GetState returns the current escrow state (shallow copy).
func (sm *StateMachine) GetState() types.EscrowState {
	return *sm.state
}

// ComputeStateRoot returns the current state root without modifying state.
func (sm *StateMachine) ComputeStateRoot() []byte {
	return ComputeStateRoot(sm.state.Balance, sm.state.HostStats, sm.state.Inferences)
}

func (sm *StateMachine) applyTx(tx *types.SubnetTx) error {
	switch inner := tx.GetTx().(type) {
	case *types.SubnetTx_StartInference:
		return sm.applyStartInference(inner.StartInference)
	case *types.SubnetTx_ConfirmStart:
		return sm.applyConfirmStart(inner.ConfirmStart)
	case *types.SubnetTx_FinishInference:
		return sm.applyFinishInference(inner.FinishInference)
	case *types.SubnetTx_Validation:
		return sm.applyValidation(inner.Validation)
	case *types.SubnetTx_ValidationVote:
		return sm.applyValidationVote(inner.ValidationVote)
	case *types.SubnetTx_TimeoutInference:
		return sm.applyTimeout(inner.TimeoutInference)
	case *types.SubnetTx_RevealSeed:
		return sm.applyRevealSeed(inner.RevealSeed)
	case *types.SubnetTx_FinalizeRound:
		_ = inner.FinalizeRound
		return sm.applyFinalizeRound()
	default:
		return types.ErrEmptyTx
	}
}

func (sm *StateMachine) applyStartInference(msg *types.MsgStartInference) error {
	if sm.state.Finalizing {
		return types.ErrSessionFinalizing
	}

	// Executor slot: group[inference_id % len(group)].SlotID
	executorSlot := sm.state.Group[msg.InferenceId%uint64(len(sm.state.Group))].SlotID

	// Reserve cost: (input_length + max_tokens) * token_price
	reservedCost := (msg.InputLength + msg.MaxTokens) * sm.state.Config.TokenPrice
	if sm.state.Balance < reservedCost {
		return types.ErrInsufficientBalance
	}

	sm.state.Balance -= reservedCost

	rec := &types.InferenceRecord{
		Status:       types.StatusPending,
		ExecutorSlot: executorSlot,
		Model:        msg.Model,
		PromptHash:   msg.PromptHash,
		InputLength:  msg.InputLength,
		MaxTokens:    msg.MaxTokens,
		ReservedCost: reservedCost,
		StartedAt:    msg.StartedAt,
		VotedSlots:   make(map[uint32]bool),
	}

	// Fast path: if executor_sig present, verify and go directly to Started.
	if len(msg.ExecutorSig) > 0 {
		receiptContent := &types.ExecutorReceiptContent{
			InferenceId: msg.InferenceId,
			PromptHash:  msg.PromptHash,
			Model:       msg.Model,
			InputLength: msg.InputLength,
			MaxTokens:   msg.MaxTokens,
			StartedAt:   msg.StartedAt,
		}
		receiptData, _ := proto.Marshal(receiptContent)

		recovered, err := sm.verifier.RecoverAddress(receiptData, msg.ExecutorSig)
		if err != nil {
			return fmt.Errorf("%w: %v", types.ErrInvalidExecutorSig, err)
		}

		expectedAddr := sm.slotToAddress[executorSlot]
		if recovered != expectedAddr {
			return fmt.Errorf("%w: expected executor %s (slot %d), got %s",
				types.ErrInvalidExecutorSig, expectedAddr, executorSlot, recovered)
		}
		rec.Status = types.StatusStarted
	}

	sm.state.Inferences[msg.InferenceId] = rec
	return nil
}

func (sm *StateMachine) applyConfirmStart(msg *types.MsgConfirmStart) error {
	rec, ok := sm.state.Inferences[msg.InferenceId]
	if !ok {
		return fmt.Errorf("%w: inference %d", types.ErrInferenceNotFound, msg.InferenceId)
	}
	if rec.Status != types.StatusPending {
		return fmt.Errorf("%w: expected pending, got %d", types.ErrInvalidTransition, rec.Status)
	}

	// Verify executor receipt.
	receiptContent := &types.ExecutorReceiptContent{
		InferenceId: msg.InferenceId,
		PromptHash:  rec.PromptHash,
		Model:       rec.Model,
		InputLength: rec.InputLength,
		MaxTokens:   rec.MaxTokens,
		StartedAt:   rec.StartedAt,
	}
	receiptData, _ := proto.Marshal(receiptContent)

	recovered, err := sm.verifier.RecoverAddress(receiptData, msg.ExecutorSig)
	if err != nil {
		return fmt.Errorf("%w: %v", types.ErrInvalidExecutorSig, err)
	}

	expectedAddr := sm.slotToAddress[rec.ExecutorSlot]
	if recovered != expectedAddr {
		return fmt.Errorf("%w: expected executor %s (slot %d), got %s",
			types.ErrInvalidExecutorSig, expectedAddr, rec.ExecutorSlot, recovered)
	}

	rec.Status = types.StatusStarted
	return nil
}

func (sm *StateMachine) applyFinishInference(msg *types.MsgFinishInference) error {
	rec, ok := sm.state.Inferences[msg.InferenceId]
	if !ok {
		return fmt.Errorf("%w: inference %d", types.ErrInferenceNotFound, msg.InferenceId)
	}
	if rec.Status != types.StatusStarted {
		return fmt.Errorf("%w: expected started, got %d", types.ErrInvalidTransition, rec.Status)
	}

	// Verify executor slot.
	if msg.ExecutorSlot != rec.ExecutorSlot {
		return fmt.Errorf("%w: expected %d, got %d", types.ErrWrongExecutorSlot, rec.ExecutorSlot, msg.ExecutorSlot)
	}

	// Verify proposer signature.
	if err := sm.verifyProposerSig(msg, msg.ProposerSig, func(m *types.MsgFinishInference) {
		m.ProposerSig = nil
	}); err != nil {
		return err
	}

	// Compute actual cost.
	actualCost := (msg.InputTokens + msg.OutputTokens) * sm.state.Config.TokenPrice
	if actualCost > rec.ReservedCost {
		return types.ErrActualCostExceedsMax
	}

	// Release surplus.
	surplus := rec.ReservedCost - actualCost
	sm.state.Balance += surplus

	rec.Status = types.StatusFinished
	rec.ResponseHash = msg.ResponseHash
	rec.InputTokens = msg.InputTokens
	rec.OutputTokens = msg.OutputTokens
	rec.ActualCost = actualCost

	// Update host stats.
	sm.state.HostStats[rec.ExecutorSlot].Cost += actualCost

	return nil
}

func (sm *StateMachine) applyValidation(msg *types.MsgValidation) error {
	rec, ok := sm.state.Inferences[msg.InferenceId]
	if !ok {
		return fmt.Errorf("%w: inference %d", types.ErrInferenceNotFound, msg.InferenceId)
	}
	if rec.Status != types.StatusFinished {
		return fmt.Errorf("%w: expected finished, got %d", types.ErrInvalidTransition, rec.Status)
	}
	if msg.ValidatorSlot == rec.ExecutorSlot {
		return types.ErrSelfValidation
	}

	// Verify proposer signature.
	if err := sm.verifyProposerSig(msg, msg.ProposerSig, func(m *types.MsgValidation) {
		m.ProposerSig = nil
	}); err != nil {
		return err
	}

	if msg.Valid {
		rec.Status = types.StatusValidated
	} else {
		rec.Status = types.StatusChallenged
	}

	return nil
}

func (sm *StateMachine) applyValidationVote(msg *types.MsgValidationVote) error {
	rec, ok := sm.state.Inferences[msg.InferenceId]
	if !ok {
		return fmt.Errorf("%w: inference %d", types.ErrInferenceNotFound, msg.InferenceId)
	}
	if rec.Status != types.StatusChallenged {
		return fmt.Errorf("%w: expected challenged, got %d", types.ErrInvalidTransition, rec.Status)
	}

	// Check duplicate vote.
	if rec.VotedSlots[msg.VoterSlot] {
		return fmt.Errorf("%w: slot %d", types.ErrDuplicateVote, msg.VoterSlot)
	}

	// Verify proposer signature.
	if err := sm.verifyProposerSig(msg, msg.ProposerSig, func(m *types.MsgValidationVote) {
		m.ProposerSig = nil
	}); err != nil {
		return err
	}

	rec.VotedSlots[msg.VoterSlot] = true
	if msg.VoteValid {
		rec.VotesValid++
	} else {
		rec.VotesInvalid++
	}

	// Check majority. Threshold: total_slots / 2
	threshold := sm.totalSlots / 2
	if rec.VotesInvalid > threshold {
		rec.Status = types.StatusInvalidated
		// Refund cost.
		sm.state.HostStats[rec.ExecutorSlot].Invalid++
		sm.state.HostStats[rec.ExecutorSlot].Cost -= rec.ActualCost
		sm.state.Balance += rec.ActualCost
	} else if rec.VotesValid > threshold {
		rec.Status = types.StatusValidated
	}

	return nil
}

func (sm *StateMachine) applyTimeout(msg *types.MsgTimeoutInference) error {
	rec, ok := sm.state.Inferences[msg.InferenceId]
	if !ok {
		return fmt.Errorf("%w: inference %d", types.ErrInferenceNotFound, msg.InferenceId)
	}

	// Validate reason matches status.
	switch msg.Reason {
	case "refused":
		if rec.Status != types.StatusPending {
			return fmt.Errorf("%w: reason=refused requires pending, got %d", types.ErrInvalidTimeoutReason, rec.Status)
		}
	case "execution":
		if rec.Status != types.StatusStarted {
			return fmt.Errorf("%w: reason=execution requires started, got %d", types.ErrInvalidTimeoutReason, rec.Status)
		}
	default:
		return fmt.Errorf("%w: unknown reason %q", types.ErrInvalidTimeoutReason, msg.Reason)
	}

	// Count accept votes and verify each signature.
	acceptCount := uint32(0)
	for _, vote := range msg.Votes {
		voteContent := &types.TimeoutVoteContent{
			EscrowId:    sm.state.EscrowID,
			InferenceId: msg.InferenceId,
			Reason:      msg.Reason,
			Accept:      vote.Accept,
		}
		voteData, _ := proto.Marshal(voteContent)

		recovered, err := sm.verifier.RecoverAddress(voteData, vote.Signature)
		if err != nil {
			return fmt.Errorf("%w: vote from slot %d: %v", types.ErrInvalidExecutorSig, vote.VoterSlot, err)
		}

		expectedAddr := sm.slotToAddress[vote.VoterSlot]
		if recovered != expectedAddr {
			return fmt.Errorf("%w: vote from slot %d: expected %s, got %s",
				types.ErrInvalidExecutorSig, vote.VoterSlot, expectedAddr, recovered)
		}

		if vote.Accept {
			acceptCount++
		}
	}

	// Check threshold.
	threshold := sm.totalSlots / 2
	if acceptCount <= threshold {
		return fmt.Errorf("%w: need >%d accept votes, got %d", types.ErrInsufficientVotes, threshold, acceptCount)
	}

	rec.Status = types.StatusTimedOut
	sm.state.HostStats[rec.ExecutorSlot].Missed++

	// Release reserved cost back to escrow.
	sm.state.Balance += rec.ReservedCost

	return nil
}

func (sm *StateMachine) applyRevealSeed(msg *types.MsgRevealSeed) error {
	// Verify proposer signature.
	if err := sm.verifyProposerSig(msg, msg.ProposerSig, func(m *types.MsgRevealSeed) {
		m.ProposerSig = nil
	}); err != nil {
		return err
	}

	// Accept but no state effect in Phase 1 (ShouldValidate deferred to Phase 4).
	return nil
}

func (sm *StateMachine) applyFinalizeRound() error {
	if sm.state.Finalizing {
		return types.ErrAlreadyFinalizing
	}
	sm.state.Finalizing = true
	return nil
}

// buildDiffContent converts a Diff to the proto DiffContent for signing.
func (sm *StateMachine) buildDiffContent(diff types.Diff) *types.DiffContent {
	return BuildDiffContent(diff.Nonce, diff.Txs)
}

// BuildDiffContent creates the proto DiffContent from nonce and txs for signing.
// Since Diff.Txs is already []*SubnetTx, no conversion is needed.
func BuildDiffContent(nonce uint64, txs []*types.SubnetTx) *types.DiffContent {
	return &types.DiffContent{
		Nonce: nonce,
		Txs:   txs,
	}
}

// verifyProposerSig is a generic helper that verifies a host-proposed tx signature.
// It clones the proto message, zeroes the sig field, marshals, recovers the address,
// and checks that the recovered address belongs to a group member.
func (sm *StateMachine) verifyProposerSig(msg proto.Message, sig []byte, clearSig any) error {
	// Clone the message and zero the signature field.
	cloned := proto.Clone(msg)

	switch fn := clearSig.(type) {
	case func(*types.MsgFinishInference):
		fn(cloned.(*types.MsgFinishInference))
	case func(*types.MsgValidation):
		fn(cloned.(*types.MsgValidation))
	case func(*types.MsgValidationVote):
		fn(cloned.(*types.MsgValidationVote))
	case func(*types.MsgRevealSeed):
		fn(cloned.(*types.MsgRevealSeed))
	default:
		return fmt.Errorf("unsupported message type for proposer sig verification")
	}

	data, err := proto.Marshal(cloned)
	if err != nil {
		return fmt.Errorf("marshal for proposer sig: %w", err)
	}

	recovered, err := sm.verifier.RecoverAddress(data, sig)
	if err != nil {
		return fmt.Errorf("%w: %v", types.ErrInvalidProposerSig, err)
	}

	if _, ok := sm.addressToSlot[recovered]; !ok {
		return fmt.Errorf("%w: address %s", types.ErrInvalidProposerSig, recovered)
	}

	return nil
}

// TotalSlots returns the number of slots in the group.
func (sm *StateMachine) TotalSlots() uint32 {
	return sm.totalSlots
}

// SlotAddress returns the validator address for a slot.
func (sm *StateMachine) SlotAddress(slotID uint32) string {
	return sm.slotToAddress[slotID]
}

// SortedSlotIDs returns slot IDs sorted ascending.
func SortedSlotIDs(group []types.SlotAssignment) []uint32 {
	ids := make([]uint32, len(group))
	for i, s := range group {
		ids[i] = s.SlotID
	}
	slices.Sort(ids)
	return ids
}
