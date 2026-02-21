package statsstorage

import "context"

// InferenceRecord is the off-chain source-of-truth record for one inference.
type InferenceRecord struct {
	InferenceID          string
	RequestedBy          string
	Model                string
	Status               string
	EpochID              uint64
	PromptTokenCount     uint64
	CompletionTokenCount uint64
	TotalTokenCount      uint64
	ActualCostInCoins    int64
	StartBlockTimestamp  int64
	EndBlockTimestamp    int64
	InferenceTimestamp   int64
}

type Summary struct {
	AiTokens             int64
	Inferences           int32
	ActualInferencesCost int64
}

type ModelSummary struct {
	Model      string
	AiTokens   int64
	Inferences int32
}

type DeveloperTimeStats struct {
	Developer string
	Stats     []InferenceRecord
}

type DeveloperEpochStats struct {
	Developer    string
	EpochID      uint64
	InferenceIDs []string
}

type DebugStats struct {
	StatsByTime  []DeveloperTimeStats
	StatsByEpoch []DeveloperEpochStats
}

// StatsStorage defines storage and read models for off-chain developer stats.
type StatsStorage interface {
	UpsertInference(ctx context.Context, rec InferenceRecord) error
	GetDeveloperInferencesByTime(ctx context.Context, developer string, timeFrom, timeTo int64) ([]InferenceRecord, error)
	GetSummaryByDeveloperEpochsBackwards(ctx context.Context, developer string, epochsN int32) (Summary, error)
	GetSummaryByEpochsBackwards(ctx context.Context, epochsN int32) (Summary, error)
	GetSummaryByTimePeriod(ctx context.Context, timeFrom, timeTo int64) (Summary, error)
	GetModelStatsByTime(ctx context.Context, timeFrom, timeTo int64) ([]ModelSummary, error)
	GetDebugStats(ctx context.Context) (DebugStats, error)
	PruneOlderThan(ctx context.Context, cutoffTimestamp int64) error
	Close()
}
