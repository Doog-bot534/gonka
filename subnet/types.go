package subnet

// ExecuteRequest contains the data needed to execute an inference.
type ExecuteRequest struct {
	InferenceID uint64
	Model       string
	Prompt      []byte
	PromptHash  []byte
	InputLength uint64
	MaxTokens   uint64
}

// ExecuteResult contains the outcome of an inference execution.
type ExecuteResult struct {
	ResponseHash []byte
	InputTokens  uint64
	OutputTokens uint64
}

// ValidateRequest contains the data needed to validate an inference.
type ValidateRequest struct {
	InferenceID  uint64
	Model        string
	PromptHash   []byte
	ResponseHash []byte
	InputTokens  uint64
	OutputTokens uint64
}

// ValidateResult contains the outcome of a validation.
type ValidateResult struct {
	Valid bool
}
