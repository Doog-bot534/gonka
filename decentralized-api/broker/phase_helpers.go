package broker

import "github.com/productscience/inference/x/inference/types"

func (b *Broker) IsInPoCGeneratePhase() bool {
	if b.phaseTracker == nil {
		return false
	}
	epochState := b.phaseTracker.GetCurrentEpochState()
	if epochState.IsNilOrNotSynced() {
		return false
	}
	// Regular PoC generation
	if epochState.CurrentPhase == types.PoCGeneratePhase {
		return true
	}
	// Confirmation PoC generation during inference phase
	if epochState.CurrentPhase == types.InferencePhase &&
		epochState.ActiveConfirmationPoCEvent != nil &&
		epochState.ActiveConfirmationPoCEvent.Phase == types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION {
		return true
	}
	return false
}

func (b *Broker) IsInPoCValidatePhase() bool {
	if b.phaseTracker == nil {
		return false
	}
	epochState := b.phaseTracker.GetCurrentEpochState()
	if epochState.IsNilOrNotSynced() {
		return false
	}
	// Regular PoC validation
	if epochState.CurrentPhase == types.PoCValidatePhase ||
		epochState.CurrentPhase == types.PoCValidateWindDownPhase {
		return true
	}
	// Confirmation PoC validation during inference phase
	if epochState.CurrentPhase == types.InferencePhase &&
		epochState.ActiveConfirmationPoCEvent != nil &&
		epochState.ActiveConfirmationPoCEvent.Phase == types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION {
		return true
	}
	return false
}
