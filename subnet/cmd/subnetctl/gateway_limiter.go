package main

import (
	"fmt"
	"sync"
)

type GatewayLimiter struct {
	mu                sync.Mutex
	maxConcurrent     int64
	maxInputTokens    int64
	inFlightRequests  int64
	inFlightInputToks int64
}

type LimiterSnapshot struct {
	InFlightRequests   int64 `json:"in_flight_requests"`
	InFlightInputTokens int64 `json:"in_flight_input_tokens"`
	MaxConcurrent      int64 `json:"max_concurrent_requests"`
	MaxInputTokens     int64 `json:"max_input_tokens_in_flight"`
}

func NewGatewayLimiter(maxConcurrent, maxInputTokens int64) *GatewayLimiter {
	return &GatewayLimiter{
		maxConcurrent:  maxConcurrent,
		maxInputTokens: maxInputTokens,
	}
}

func (l *GatewayLimiter) Snapshot() LimiterSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	return LimiterSnapshot{
		InFlightRequests:    l.inFlightRequests,
		InFlightInputTokens: l.inFlightInputToks,
		MaxConcurrent:       l.maxConcurrent,
		MaxInputTokens:      l.maxInputTokens,
	}
}

func (l *GatewayLimiter) Acquire(inputTokens int64) error {
	if inputTokens <= 0 {
		inputTokens = 1
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.maxConcurrent > 0 && l.inFlightRequests+1 > l.maxConcurrent {
		return fmt.Errorf("rate limit exceeded: too many concurrent requests")
	}
	if l.maxInputTokens > 0 && l.inFlightInputToks+inputTokens > l.maxInputTokens {
		return fmt.Errorf("rate limit exceeded: too many input tokens in flight")
	}

	l.inFlightRequests++
	l.inFlightInputToks += inputTokens
	return nil
}

func (l *GatewayLimiter) Release(inputTokens int64) {
	if inputTokens <= 0 {
		inputTokens = 1
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.inFlightRequests--
	if l.inFlightRequests < 0 {
		l.inFlightRequests = 0
	}
	l.inFlightInputToks -= inputTokens
	if l.inFlightInputToks < 0 {
		l.inFlightInputToks = 0
	}
}
