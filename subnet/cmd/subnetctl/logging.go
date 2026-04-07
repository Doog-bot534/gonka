package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"
)

type requestLogContext struct {
	RequestID string
}

type requestLogContextKey struct{}

var requestLogSeq atomic.Uint64

func ensureRequestLogContext(ctx context.Context) (context.Context, requestLogContext) {
	if existing, ok := requestLogFromContext(ctx); ok {
		return ctx, existing
	}
	req := requestLogContext{
		RequestID: fmt.Sprintf("req-%d-%d", time.Now().UnixNano(), requestLogSeq.Add(1)),
	}
	return context.WithValue(ctx, requestLogContextKey{}, req), req
}

func requestLogFromContext(ctx context.Context) (requestLogContext, bool) {
	if ctx == nil {
		return requestLogContext{}, false
	}
	req, ok := ctx.Value(requestLogContextKey{}).(requestLogContext)
	return req, ok && req.RequestID != ""
}

func logRequestStage(ctx context.Context, stage string, kv ...any) {
	fields := []string{fmt.Sprintf("stage=%s", stage)}
	if req, ok := requestLogFromContext(ctx); ok {
		fields = append([]string{fmt.Sprintf("request=%s", req.RequestID)}, fields...)
	}
	fields = append(fields, formatLogFields(kv...)...)
	log.Print(strings.Join(fields, " "))
}

func logInferenceStage(ctx context.Context, escrowID string, nonce uint64, stage string, kv ...any) {
	fields := []string{
		fmt.Sprintf("stage=%s", stage),
		fmt.Sprintf("escrow=%s", escrowID),
		fmt.Sprintf("nonce=%d", nonce),
	}
	if req, ok := requestLogFromContext(ctx); ok {
		fields = append([]string{fmt.Sprintf("request=%s", req.RequestID)}, fields...)
	}
	fields = append(fields, formatLogFields(kv...)...)
	log.Print(strings.Join(fields, " "))
}

func formatLogFields(kv ...any) []string {
	fields := make([]string, 0, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		key := fmt.Sprintf("field_%d", i)
		if s, ok := kv[i].(string); ok && s != "" {
			key = s
		}
		value := "<missing>"
		if i+1 < len(kv) {
			value = fmt.Sprint(kv[i+1])
		}
		fields = append(fields, fmt.Sprintf("%s=%s", key, sanitizeLogValue(value)))
	}
	return fields
}

func sanitizeLogValue(v string) string {
	if v == "" {
		return `""`
	}
	if strings.ContainsAny(v, " \t\n\r\"") {
		return fmt.Sprintf("%q", v)
	}
	return v
}
