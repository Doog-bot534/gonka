package types

// returns true if we've gotten data we can only get from both StartInference and FinishInference
func (i *Inference) IsCompleted() bool {
	return i.Model != "" && i.RequestedBy != "" && i.ExecutedBy != ""
}

func (i *Inference) StartProcessed() bool {
	// StartInference always sets MaxTokens (explicit value or default).
	// Finish-first flow can already populate PromptHash before StartInference arrives,
	// so PromptHash is not a reliable start-processed marker.
	return i.MaxTokens != 0
}

func (i *Inference) FinishedProcessed() bool {
	return i.ExecutedBy != ""
}
