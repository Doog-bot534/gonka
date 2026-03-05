package public

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestV2RequestDeduper_StreamingFollowerGetsBufferedHistoryAndLiveTail(t *testing.T) {
	deduper := newV2RequestDeduper()
	streamBody := "data: chunk-1\n\ndata: chunk-2\n\n"

	executor := func() (*http.Response, error) {
		pr, pw := io.Pipe()
		go func() {
			defer pw.Close()
			_, _ = pw.Write([]byte("data: chunk-1\n\n"))
			time.Sleep(50 * time.Millisecond)
			_, _ = pw.Write([]byte("data: chunk-2\n\n"))
		}()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body: pr,
		}, nil
	}

	leaderResp, err := deduper.execute("escrow-1:1", "hash-1", executor)
	require.NoError(t, err)
	require.NotNil(t, leaderResp)

	leaderReadDone := make(chan string, 1)
	go func() {
		defer leaderResp.Body.Close()
		body, _ := io.ReadAll(leaderResp.Body)
		leaderReadDone <- string(body)
	}()

	time.Sleep(10 * time.Millisecond) // Join after stream started but before completion.
	followerResp, err := deduper.execute("escrow-1:1", "hash-1", executor)
	require.NoError(t, err)
	require.NotNil(t, followerResp)
	defer followerResp.Body.Close()

	followerBodyBytes, err := io.ReadAll(followerResp.Body)
	require.NoError(t, err)
	leaderBody := <-leaderReadDone

	require.Equal(t, streamBody, leaderBody)
	require.Equal(t, streamBody, string(followerBodyBytes))
}

func TestV2RequestDeduper_StreamingLateFollowerGetsBufferedHistory(t *testing.T) {
	deduper := newV2RequestDeduper()
	streamBody := "data: done-1\n\ndata: done-2\n\n"

	executor := func() (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body: io.NopCloser(strings.NewReader(streamBody)),
		}, nil
	}

	leaderResp, err := deduper.execute("escrow-1:2", "hash-2", executor)
	require.NoError(t, err)
	require.NotNil(t, leaderResp)
	leaderBody, err := io.ReadAll(leaderResp.Body)
	require.NoError(t, err)
	require.NoError(t, leaderResp.Body.Close())
	require.Equal(t, streamBody, string(leaderBody))

	lateFollowerResp, err := deduper.execute("escrow-1:2", "hash-2", executor)
	require.NoError(t, err)
	require.NotNil(t, lateFollowerResp)
	defer lateFollowerResp.Body.Close()

	lateFollowerBody, err := io.ReadAll(lateFollowerResp.Body)
	require.NoError(t, err)
	require.Equal(t, streamBody, string(lateFollowerBody))
}

func TestV2RequestDeduper_ReplaysCompletedErrorForSubsequentDuplicate(t *testing.T) {
	deduper := newV2RequestDeduper()
	expectedErr := errors.New("intended executor unavailable")

	executor := func() (*http.Response, error) {
		return nil, expectedErr
	}

	_, err := deduper.execute("escrow-1:3", "hash-3", executor)
	require.ErrorIs(t, err, expectedErr)

	// Subsequent duplicate after completion should replay the same deterministic failure.
	_, err = deduper.execute("escrow-1:3", "hash-3", executor)
	require.ErrorIs(t, err, expectedErr)
}

func TestV2RequestDeduper_StreamingContinuesAfterLeaderDisconnect(t *testing.T) {
	deduper := newV2RequestDeduper()
	streamBody := "data: alpha\n\ndata: beta\n\n"

	var once sync.Once
	executor := func() (*http.Response, error) {
		pr, pw := io.Pipe()
		once.Do(func() {
			go func() {
				defer pw.Close()
				_, _ = pw.Write([]byte("data: alpha\n\n"))
				time.Sleep(30 * time.Millisecond)
				_, _ = pw.Write([]byte("data: beta\n\n"))
			}()
		})
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
			},
			Body: pr,
		}, nil
	}

	leaderResp, err := deduper.execute("escrow-1:4", "hash-4", executor)
	require.NoError(t, err)
	require.NotNil(t, leaderResp)

	// Simulate leader client disconnecting early.
	leaderChunk := make([]byte, len("data: alpha\n\n"))
	_, _ = io.ReadFull(leaderResp.Body, leaderChunk)
	require.NoError(t, leaderResp.Body.Close())

	// Allow upstream pump to complete in background.
	time.Sleep(60 * time.Millisecond)

	followerResp, err := deduper.execute("escrow-1:4", "hash-4", executor)
	require.NoError(t, err)
	require.NotNil(t, followerResp)
	defer followerResp.Body.Close()

	followerBody, err := io.ReadAll(followerResp.Body)
	require.NoError(t, err)
	require.Equal(t, streamBody, string(followerBody))
}
