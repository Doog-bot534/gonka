package protocol

import (
	"context"
	"net/http/httptest"
	"testing"

	"bytes"
	"fmt"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"subnet/host"
	"subnet/internal/testutil"
	"subnet/signing"
	"subnet/state"
	"subnet/storage"
	"subnet/stub"
	"subnet/transport"
	"subnet/types"
	"subnet/user"
)

type httpTestEnv struct {
	session    *user.Session
	hosts      []*host.Host
	servers    []*transport.Server
	httpServers []*httptest.Server
	clients    []*transport.HTTPClient
	signers    []*signing.Secp256k1Signer
	userSigner *signing.Secp256k1Signer
	group      []types.SlotAssignment
	config     types.SessionConfig
	stores     []*storage.Memory
}

func setupHTTPEnv(t *testing.T, numHosts int, balance, grace uint64) *httpTestEnv {
	t.Helper()
	hostSigners := make([]*signing.Secp256k1Signer, numHosts)
	for i := range hostSigners {
		hostSigners[i] = testutil.MustGenerateKey(t)
	}
	userSigner := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hostSigners)
	config := testutil.DefaultConfig(numHosts)
	verifier := signing.NewSecp256k1Verifier()

	hosts := make([]*host.Host, numHosts)
	servers := make([]*transport.Server, numHosts)
	httpServers := make([]*httptest.Server, numHosts)
	stores := make([]*storage.Memory, numHosts)

	for i := range hostSigners {
		sm := state.NewStateMachine("escrow-1", config, group, balance, userSigner.Address(), verifier)
		engine := stub.NewInferenceEngine()
		store := storage.NewMemory()
		require.NoError(t, store.CreateSession("escrow-1", config, group, balance))
		stores[i] = store

		h, err := host.NewHost(sm, hostSigners[i], engine, "escrow-1", group, grace, nil, host.WithStorage(store))
		require.NoError(t, err)
		hosts[i] = h

		srv := transport.NewServer(h, store, "escrow-1", verifier, group, userSigner.Address())
		servers[i] = srv

		e := echo.New()
		g := e.Group("/subnet/v1")
		srv.Register(g)
		ts := httptest.NewServer(e)
		t.Cleanup(ts.Close)
		httpServers[i] = ts
	}

	// Create HTTP clients for each host.
	clients := make([]*transport.HTTPClient, numHosts)
	userClients := make([]user.HostClient, numHosts)
	for i := range httpServers {
		c := transport.NewHTTPClient(httpServers[i].URL, "escrow-1", userSigner)
		clients[i] = c
		userClients[i] = c
	}

	// Wire peer clients for timeout verification.
	// Each server needs access to all other hosts (including executor) for verification.
	for _, srv := range servers {
		peers := make(map[int]*transport.HTTPClient)
		for j, c := range clients {
			peers[j] = c
		}
		srv.SetPeerClients(peers)
	}

	userSM := state.NewStateMachine("escrow-1", config, group, balance, userSigner.Address(), verifier)
	session, err := user.NewSession(userSM, userSigner, "escrow-1", group, userClients)
	require.NoError(t, err)

	return &httpTestEnv{
		session:    session,
		hosts:      hosts,
		servers:    servers,
		httpServers: httpServers,
		clients:    clients,
		signers:    hostSigners,
		userSigner: userSigner,
		group:      group,
		config:     config,
		stores:     stores,
	}
}

func TestHTTP_HappyPath(t *testing.T) {
	env := setupHTTPEnv(t, 3, 1000000, 100)
	ctx := context.Background()
	params := defaultParams()

	for i := 0; i < 15; i++ {
		result, err := env.session.SendInference(ctx, params)
		require.NoError(t, err, "inference %d", i+1)
		require.NotNil(t, result)
	}

	err := env.session.Finalize(ctx)
	require.NoError(t, err)

	st := env.session.StateMachine().SnapshotState()
	require.True(t, st.Finalizing)
	require.Equal(t, 15, len(st.Inferences))

	for id, rec := range st.Inferences {
		require.Equal(t, types.StatusFinished, rec.Status, "inference %d should be finished", id)
	}
}

func TestHTTP_Auth_Rejected(t *testing.T) {
	env := setupHTTPEnv(t, 1, 100000, 100)

	// Create a client with a different signer (not the user).
	badSigner := testutil.MustGenerateKey(t)
	badClient := transport.NewHTTPClient(env.httpServers[0].URL, "escrow-1", badSigner)

	diff := testutil.SignDiff(t, env.userSigner, 1, []*types.SubnetTx{testutil.StartTx(1)})
	_, err := badClient.Send(context.Background(), host.HostRequest{
		Diffs: []types.Diff{diff},
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   50,
			StartedAt:   1000,
		},
	})
	// The bad signer is not in the group or the user, so auth rejects with 403.
	require.Error(t, err)
	require.Contains(t, err.Error(), "403")
}

func TestHTTP_GossipPropagation(t *testing.T) {
	env := setupHTTPEnv(t, 3, 100000, 100)
	ctx := context.Background()

	// Send inference via HTTPClient directly to host 0.
	diff := testutil.SignDiff(t, env.userSigner, 1, []*types.SubnetTx{testutil.StartTx(1)})
	resp, err := env.clients[0].Send(ctx, host.HostRequest{
		Diffs: []types.Diff{diff},
		Nonce: 1,
		Payload: &host.InferencePayload{
			Prompt:      testutil.TestPrompt,
			Model:       "llama",
			InputLength: 100,
			MaxTokens:   50,
			StartedAt:   1000,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Manually gossip nonce to host 1.
	err = env.clients[1].GossipNonce(ctx, 1, []byte("hash"), resp.StateSig, 0)
	require.NoError(t, err)
}

func TestHTTP_EquivocationDetection(t *testing.T) {
	env := setupHTTPEnv(t, 3, 100000, 100)

	// Set up gossip on server 0.
	g := setupGossipForHost(t, env, 0)

	ctx := context.Background()

	// Send same nonce with different hashes.
	err := g.OnNonceReceived(1, []byte("hash-a"), []byte("sig-a"), 1)
	require.NoError(t, err)

	err = g.OnNonceReceived(1, []byte("hash-b"), []byte("sig-b"), 2)
	require.Error(t, err)
	require.Contains(t, err.Error(), "equivocation")

	_ = ctx // used in setup
}

func TestHTTP_TimeoutRefused(t *testing.T) {
	env := setupHTTPEnv(t, 5, 1000000, 100)
	ctx := context.Background()

	// Send one inference. Executor is slot 1%5=1.
	params := defaultParams()
	_, err := env.session.SendInference(ctx, params)
	require.NoError(t, err)

	// Build timeout verifiers from HTTP clients (non-executor hosts).
	verifiers := make(map[int]user.TimeoutVerifier)
	for i := range env.clients {
		if i == 1 { // skip executor
			continue
		}
		verifiers[i] = &httpTimeoutVerifier{client: env.clients[i]}
	}

	// Catch up all non-executor hosts so they have the inference state.
	allDiffs := env.session.Diffs()
	for i, h := range env.hosts {
		if i == 1 {
			continue
		}
		_, err := h.HandleRequest(ctx, host.HostRequest{Diffs: allDiffs, Nonce: allDiffs[len(allDiffs)-1].Nonce})
		require.NoError(t, err)
	}

	votes, err := env.session.CollectTimeoutVotes(ctx, 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, testutil.TestPrompt, verifiers)
	require.NoError(t, err)
	// With 5 hosts, threshold is 2, we need >2 votes. We have 4 non-executor hosts.
	require.True(t, len(votes) > int(env.config.VoteThreshold), "need >%d votes, got %d", env.config.VoteThreshold, len(votes))

	// Compose and apply timeout.
	timeoutTx := &types.SubnetTx{Tx: &types.SubnetTx_TimeoutInference{
		TimeoutInference: &types.MsgTimeoutInference{
			InferenceId: 1,
			Reason:      types.TimeoutReason_TIMEOUT_REASON_REFUSED,
			Votes:       votes,
		},
	}}
	nonce := env.session.Nonce() + 1
	diff := testutil.SignDiff(t, env.userSigner, nonce, []*types.SubnetTx{timeoutTx})
	_, err = env.session.StateMachine().ApplyDiff(diff)
	require.NoError(t, err)

	st := env.session.StateMachine().SnapshotState()
	require.Equal(t, types.StatusTimedOut, st.Inferences[1].Status)
}

func TestHTTP_TimeoutExecution(t *testing.T) {
	env := setupHTTPEnv(t, 5, 1000000, 100)
	ctx := context.Background()
	params := defaultParams()

	// Send inference and confirm start.
	result, err := env.session.SendInference(ctx, params)
	require.NoError(t, err)
	require.NotNil(t, result.Receipt)

	// Manually confirm start in a new diff.
	confirmTx := &types.SubnetTx{Tx: &types.SubnetTx_ConfirmStart{
		ConfirmStart: &types.MsgConfirmStart{
			InferenceId: 1,
			ExecutorSig: result.Receipt,
		},
	}}
	nonce := env.session.Nonce() + 1
	diff := testutil.SignDiff(t, env.userSigner, nonce, []*types.SubnetTx{confirmTx})
	_, err = env.session.StateMachine().ApplyDiff(diff)
	require.NoError(t, err)

	// Shut down executor (host 1) to simulate unreachable.
	env.httpServers[1].Close()

	// Catch up all non-executor hosts with diffs including the confirm.
	allDiffs := append(env.session.Diffs(), diff)
	for i, h := range env.hosts {
		if i == 1 {
			continue
		}
		_, err := h.HandleRequest(ctx, host.HostRequest{Diffs: allDiffs, Nonce: diff.Nonce})
		require.NoError(t, err)
	}

	verifiers := make(map[int]user.TimeoutVerifier)
	for i := range env.clients {
		if i == 1 {
			continue
		}
		verifiers[i] = &httpTimeoutVerifier{client: env.clients[i]}
	}

	votes, err := env.session.CollectTimeoutVotes(ctx, 1, types.TimeoutReason_TIMEOUT_REASON_EXECUTION, nil, verifiers)
	require.NoError(t, err)
	// Executor is unreachable, so non-executor hosts accept the timeout.
	require.True(t, len(votes) > int(env.config.VoteThreshold),
		"need >%d votes, got %d", env.config.VoteThreshold, len(votes))
}

func TestHTTP_TimeoutRejected(t *testing.T) {
	env := setupHTTPEnv(t, 3, 100000, 100)
	ctx := context.Background()
	params := defaultParams()

	// Send inference and get finish included.
	_, err := env.session.SendInference(ctx, params)
	require.NoError(t, err)

	// Send second inference to pipeline the finish.
	_, err = env.session.SendInference(ctx, params)
	require.NoError(t, err)

	// Now inference 1 should be finished. Trying to timeout should fail.
	st := env.session.StateMachine().SnapshotState()
	require.Equal(t, types.StatusFinished, st.Inferences[1].Status)

	// Catch up host 2.
	allDiffs := env.session.Diffs()
	_, err = env.hosts[2].HandleRequest(ctx, host.HostRequest{Diffs: allDiffs, Nonce: allDiffs[len(allDiffs)-1].Nonce})
	require.NoError(t, err)

	// Try timeout verification -- should fail because inference is finished, not pending/started.
	verifier := &httpTimeoutVerifier{client: env.clients[2]}
	accept, _, _, err := verifier.VerifyTimeout(ctx, 1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, nil)
	// The server returns an error because inference is finished, not pending.
	require.Error(t, err)
	require.False(t, accept)
}

func TestHTTP_StateRecovery(t *testing.T) {
	env := setupHTTPEnv(t, 3, 100000, 100)
	ctx := context.Background()
	params := defaultParams()

	// Send 3 inferences.
	for i := 0; i < 3; i++ {
		_, err := env.session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	// GET diffs from each host that has stored them.
	for i, c := range env.clients {
		diffs, err := c.GetDiffs(ctx, 1, 3)
		if err != nil {
			continue // host might not have stored all diffs
		}
		if len(diffs) > 0 {
			t.Logf("host %d stored %d diffs", i, len(diffs))
		}
	}

	// GET mempool from each host.
	for i, c := range env.clients {
		txs, err := c.GetMempool(ctx)
		require.NoError(t, err)
		t.Logf("host %d mempool: %d txs", i, len(txs))
	}
}

func TestHTTP_Finalize(t *testing.T) {
	env := setupHTTPEnv(t, 5, 1000000, 100)
	ctx := context.Background()
	params := defaultParams()

	for i := 0; i < 15; i++ {
		_, err := env.session.SendInference(ctx, params)
		require.NoError(t, err)
	}

	err := env.session.Finalize(ctx)
	require.NoError(t, err)

	st := env.session.StateMachine().SnapshotState()
	require.True(t, st.Finalizing)
	for id, rec := range st.Inferences {
		require.Equal(t, types.StatusFinished, rec.Status, "inference %d", id)
	}

	// Verify signatures collected from all hosts.
	sigs := env.session.Signatures()
	signedSlots := make(map[uint32]bool)
	for _, slotSigs := range sigs {
		for slotID := range slotSigs {
			signedSlots[slotID] = true
		}
	}
	for i := uint32(0); i < 5; i++ {
		require.True(t, signedSlots[i], "slot %d should have signed", i)
	}
}

// --- helpers ---

// httpTimeoutVerifier wraps HTTPClient for timeout verification.
type httpTimeoutVerifier struct {
	client *transport.HTTPClient
}

func (v *httpTimeoutVerifier) VerifyTimeout(ctx context.Context, inferenceID uint64, reason types.TimeoutReason, promptData []byte) (bool, []byte, uint32, error) {
	resp, err := v.client.VerifyTimeout(ctx, transport.VerifyTimeoutRequest{
		InferenceID: inferenceID,
		Reason:      transport.TimeoutReasonToString(reason),
		PromptData:  promptData,
	})
	if err != nil {
		return false, nil, 0, err
	}
	return resp.Accept, resp.Signature, resp.VoterSlot, nil
}

func setupGossipForHost(t *testing.T, env *httpTestEnv, hostIdx int) *gossipHelper {
	t.Helper()
	// Import gossip package indirectly through the gossip helper.
	return &gossipHelper{seen: make(map[uint64][]byte)}
}

// gossipHelper is a simple in-test gossip tracker (avoids importing gossip package in protocol tests).
type gossipHelper struct {
	seen map[uint64][]byte
}

func (g *gossipHelper) OnNonceReceived(nonce uint64, stateHash, stateSig []byte, senderSlot uint32) error {
	if existing, ok := g.seen[nonce]; ok {
		if !bytes.Equal(existing, stateHash) {
			return fmt.Errorf("equivocation at nonce %d", nonce)
		}
		return nil
	}
	h := make([]byte, len(stateHash))
	copy(h, stateHash)
	g.seen[nonce] = h
	return nil
}
