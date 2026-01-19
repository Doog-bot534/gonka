package types

// returns true if we've gotten data we can only get from both StartInference and FinishInference
func (i *Inference) IsCompleted() bool {
	return i.Model != "" && i.RequestedBy != "" && i.ExecutedBy != ""
}

func (i *Inference) StartProcessed() bool {
	// StartInference is considered processed once we have a start block height.
	// This must NOT depend on PromptHash, because FinishInference can arrive before StartInference
	// and still carry PromptHash (we want to store it for cross-message consistency checks).
	return i.StartBlockHeight != 0 || i.StartBlockTimestamp != 0
}

func (i *Inference) FinishedProcessed() bool {
	return i.ExecutedBy != ""
}
