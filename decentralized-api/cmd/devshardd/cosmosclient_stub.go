package main

import (
	"context"
	"fmt"

	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	sdkclient "github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/golang/protobuf/proto"
	igniteclient "github.com/ignite/cli/v28/ignite/pkg/cosmosclient"
	"github.com/productscience/inference/api/inference/inference"
	blstypes "github.com/productscience/inference/x/bls/types"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	restrictionstypes "github.com/productscience/inference/x/restrictions/types"

	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient"
)

// queryOnlyCosmosClient implements cosmosclient.CosmosMessageClient for
// devshardd. It is intentionally read-only and signing-only: every
// transaction-sending method panics. devshardd never writes to mainnet, so
// those paths must not be reached.
//
// This avoids pulling in NATS, JetStream, and tx_manager -- everything
// dapi's main cosmosclient constructor sets up for its tx pipeline. That
// pipeline would conflict with dapi's own tx_manager if two processes shared
// the same NATS, and is unnecessary for a host that only reads chain state
// and signs payloads.
type queryOnlyCosmosClient struct {
	ctx          context.Context
	ignite       *igniteclient.Client // ignite cosmosclient, exposes Context() and AccountRegistry
	apiAccount   apiconfig.ApiAccount
	accountAddr  string
	signerAddr   string
	accountPub   cryptotypes.PubKey
	signerPub    cryptotypes.PubKey
}

// newQueryOnlyCosmosClient builds a read-only client from an already-
// constructed ignite cosmosclient and apiAccount. The ignite client owns the
// RPC connection; we just delegate query-client construction to its
// sdkclient.Context.
func newQueryOnlyCosmosClient(
	ctx context.Context,
	ignite *igniteclient.Client,
	apiAccount apiconfig.ApiAccount,
) (*queryOnlyCosmosClient, error) {
	accountAddr, err := apiAccount.AccountAddressBech32()
	if err != nil {
		return nil, fmt.Errorf("account address: %w", err)
	}
	signerAddr, err := apiAccount.SignerAddressBech32()
	if err != nil {
		return nil, fmt.Errorf("signer address: %w", err)
	}
	signerPub, err := apiAccount.SignerAccount.Record.GetPubKey()
	if err != nil {
		return nil, fmt.Errorf("signer pubkey: %w", err)
	}
	return &queryOnlyCosmosClient{
		ctx:         ctx,
		ignite:      ignite,
		apiAccount:  apiAccount,
		accountAddr: accountAddr,
		signerAddr:  signerAddr,
		accountPub:  apiAccount.AccountKey,
		signerPub:   signerPub,
	}, nil
}

// ---------- implemented methods ----------

func (c *queryOnlyCosmosClient) NewInferenceQueryClient() inferencetypes.QueryClient {
	return inferencetypes.NewQueryClient(c.ignite.Context())
}

func (c *queryOnlyCosmosClient) GetAccountAddress() string {
	return c.accountAddr
}

func (c *queryOnlyCosmosClient) GetSignerAddress() string {
	return c.signerAddr
}

func (c *queryOnlyCosmosClient) GetKeyring() *keyring.Keyring {
	kr := c.ignite.AccountRegistry.Keyring
	return &kr
}

func (c *queryOnlyCosmosClient) GetApiAccount() apiconfig.ApiAccount {
	return c.apiAccount
}

func (c *queryOnlyCosmosClient) GetClientContext() sdkclient.Context {
	return c.ignite.Context()
}

func (c *queryOnlyCosmosClient) GetContext() context.Context {
	return c.ctx
}

func (c *queryOnlyCosmosClient) GetAddress() string {
	return c.accountAddr
}

func (c *queryOnlyCosmosClient) GetAccountPubKey() cryptotypes.PubKey {
	return c.accountPub
}

func (c *queryOnlyCosmosClient) GetSignerPubKey() cryptotypes.PubKey {
	return c.signerPub
}

// ---------- panic stubs ----------
//
// devshardd is a host-side process. It reads chain state and signs devshard
// protocol messages, but never sends transactions to mainnet. Reaching any of
// these methods is a programming error in a devshardd code path.

const notImplemented = "queryOnlyCosmosClient: devshardd must not send transactions to mainnet"

func (c *queryOnlyCosmosClient) SignBytes(_ []byte) ([]byte, error) {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) DecryptBytes(_ []byte) ([]byte, error) {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) EncryptBytes(_ []byte) ([]byte, error) {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) StartInference(_ *inference.MsgStartInference) error {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) FinishInference(_ *inference.MsgFinishInference) error {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) ReportValidation(_ *inference.MsgValidation) error {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) SubmitNewUnfundedParticipant(_ *inference.MsgSubmitNewUnfundedParticipant) error {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) SubmitPocValidationsV2(_ *inference.MsgSubmitPocValidationsV2) error {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) SubmitPoCV2StoreCommit(_ *inference.MsgPoCV2StoreCommit) error {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) SubmitMLNodeWeightDistribution(_ *inference.MsgMLNodeWeightDistribution) error {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) SubmitSeed(_ *inference.MsgSubmitSeed) error {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) ClaimRewards(_ *inference.MsgClaimRewards) error {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) SubmitUnitOfComputePriceProposal(_ *inference.MsgSubmitUnitOfComputePriceProposal) error {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) BridgeExchange(_ *inferencetypes.MsgBridgeExchange) error {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) GetBridgeAddresses(_ context.Context, _ string) ([]inferencetypes.BridgeContractAddress, error) {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) NewCometQueryClient() cmtservice.ServiceClient {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) BankBalances(_ context.Context, _ string) ([]sdk.Coin, error) {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) SendTransactionAsyncWithRetry(_ sdk.Msg, _ ...int64) (*sdk.TxResponse, error) {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) SendTransactionAsyncNoRetry(_ sdk.Msg) (*sdk.TxResponse, error) {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) SendTransactionSyncNoRetry(_ proto.Message, _ proto.Message) error {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) Status(_ context.Context) (*ctypes.ResultStatus, error) {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) SubmitDealerPart(_ *blstypes.MsgSubmitDealerPart) error {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) RespondDealerComplaints(_ *blstypes.MsgRespondDealerComplaints) error {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) SubmitVerificationVector(_ *blstypes.MsgSubmitVerificationVector) (*sdk.TxResponse, error) {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) SubmitGroupKeyValidationSignature(_ *blstypes.MsgSubmitGroupKeyValidationSignature) error {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) SubmitPartialSignature(_ []byte, _ []uint32, _ []byte) error {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) NewBLSQueryClient() blstypes.QueryClient {
	panic(notImplemented)
}

func (c *queryOnlyCosmosClient) NewRestrictionsQueryClient() restrictionstypes.QueryClient {
	panic(notImplemented)
}

// compile-time guard: queryOnlyCosmosClient must satisfy the full
// CosmosMessageClient interface. Panicking stubs are still satisfying.
var _ cosmosclient.CosmosMessageClient = (*queryOnlyCosmosClient)(nil)
