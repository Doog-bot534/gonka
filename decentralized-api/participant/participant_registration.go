package participant

import (
	"bytes"
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/server/public_entities"
	"decentralized-api/logging"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/cometbft/cometbft/crypto"
	rpcclient "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/productscience/inference/x/inference/types"
)

func participantExistsWithWait(recorder cosmosclient.CosmosMessageClient, chainNodeUrl string) (bool, error) {
	client, err := cosmosclient.NewRpcClient(chainNodeUrl)
	if err != nil {
		return false, fmt.Errorf("failed to create tendermint RPC client: %w", err)
	}
	if err := waitForFirstBlock(client, 1*time.Minute); err != nil {
		return false, fmt.Errorf("chain failed to start: %w", err)
	}

	return participantExists(recorder)
}

func participantExists(recorder cosmosclient.CosmosMessageClient) (bool, error) {
	queryClient := recorder.NewInferenceQueryClient()
	request := &types.QueryGetParticipantRequest{Index: recorder.GetAccountAddress()}

	// TODO: check participant state, compute diff and update?
	// 	Or implement some ways to periodically (or by request) update the participant state
	response, err := queryClient.Participant(recorder.GetContext(), request)
	if err != nil {
		if strings.Contains(err.Error(), "code = NotFound") {
			logging.Info("Participant does not exist", types.Participants, "Address", recorder.GetAccountAddress(), "err", err)
			return false, nil
		} else {
			return false, err
		}
	}

	_ = response

	return true, nil
}

// An alternative could be to always submit a new participant
//
//	and let the chain decide if it's a new or existing participant?
//
// Or if it's a genesis participant just submit it again if error is "block < 0"?
func waitForFirstBlock(client *rpcclient.HTTP, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for first block")
		default:
			status, err := client.Status(ctx)
			if err != nil {
				logging.Debug("Waiting for chain to start...", types.System, "error", err)
				time.Sleep(1 * time.Second)
				continue
			}
			if status.SyncInfo.LatestBlockHeight > 0 {
				return nil
			}
			time.Sleep(1 * time.Second)
		}
	}
}

func RegisterParticipantIfNeeded(recorder cosmosclient.CosmosMessageClient, config *apiconfig.ConfigManager) error {
	isTest := os.Getenv("TESTS") == "true"
	if !isTest {
		return nil
	}

	logging.Info("[TEST ONLY] Registering participant", types.Participants, "isTest", isTest)

	if config.GetChainNodeConfig().IsGenesis {
		// Genesis participants are pre-registered through the genesis ceremony process
		// Load worker key from file generated during gentx
		logging.Info("Loading genesis participant worker key from file", types.Participants)
		if err := loadGenesisWorkerKey(config); err != nil {
			return fmt.Errorf("Failed to load genesis worker key: %w", err)
		}
		return nil
	} else {
		return registerJoiningParticipant(recorder, config)
	}
}

func loadGenesisWorkerKey(configManager *apiconfig.ConfigManager) error {
	// Worker key file is created during gentx in the node's config directory
	// Path: ~/.inference/config/worker_key.json
	homeDir := os.Getenv("HOME")
	if homeDir == "" {
		homeDir = "/root"
	}
	workerKeyFile := fmt.Sprintf("%s/.inference/config/worker_key.json", homeDir)
	
	data, err := os.ReadFile(workerKeyFile)
	if err != nil {
		return fmt.Errorf("failed to read worker key file %s: %w", workerKeyFile, err)
	}
	
	var workerKeyData map[string]string
	if err := json.Unmarshal(data, &workerKeyData); err != nil {
		return fmt.Errorf("failed to unmarshal worker key data: %w", err)
	}
	
	publicKey, ok := workerKeyData["public_key"]
	if !ok {
		return fmt.Errorf("public_key not found in worker key file")
	}
	
	privateKey, ok := workerKeyData["private_key"]
	if !ok {
		return fmt.Errorf("private_key not found in worker key file")
	}
	
	// Store in config
	cfg := apiconfig.MLNodeKeyConfig{
		WorkerPublicKey:  publicKey,
		WorkerPrivateKey: privateKey,
	}
	if err := configManager.SetMLNodeKeyConfig(cfg); err != nil {
		return fmt.Errorf("failed to set ML node key config: %w", err)
	}
	
	logging.Info("Successfully loaded genesis worker key", types.Participants, 
		"publicKey", publicKey, "file", workerKeyFile)
	
	return nil
}

func registerJoiningParticipant(recorder cosmosclient.CosmosMessageClient, configManager *apiconfig.ConfigManager) error {
	if exists, err := participantExistsWithWait(recorder, configManager.GetChainNodeConfig().Url); exists {
		logging.Info("Participant already exists, skipping registration", types.Participants)
		return nil
	} else if err != nil {
		return fmt.Errorf("Failed to check if participant exists: %w", err)
	}

	validatorKey, err := getValidatorKey(configManager.GetChainNodeConfig().Url)
	if err != nil {
		return err
	}
	validatorKeyString := keyToString(validatorKey)

	workerKey, err := configManager.CreateWorkerKey()
	if err != nil {
		return fmt.Errorf("Failed to create worker key: %w", err)
	}

	address := recorder.GetAccountAddress()
	pubKey := recorder.GetAccountPubKey()
	pubKeyString := keyToStringFromBytes(pubKey.Bytes())

	logging.Info(
		"Registering joining participant",
		types.Participants, "validatorKey", validatorKeyString,
		"Url", configManager.GetApiConfig().PublicUrl,
		"Address", address,
		"PubKey", pubKeyString,
	)

	requestBody := public_entities.SubmitUnfundedNewParticipantDto{
		Address:      address,
		Url:          configManager.GetApiConfig().PublicUrl,
		ValidatorKey: validatorKeyString,
		PubKey:       pubKeyString,
		WorkerKey:    workerKey,
	}

	requestUrl, err := url.JoinPath(configManager.GetChainNodeConfig().SeedApiUrl, "/v1/participants")
	if err != nil {
		return fmt.Errorf("failed to join URL path: %w", err)
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, requestUrl, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	logging.Info("Sending request to seed node", types.Participants, "url", requestUrl)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("received non-OK response: %s", resp.Status)
	}
	return nil
}

func keyToString(key crypto.PubKey) string {
	if key == nil {
		return ""
	}
	return keyToStringFromBytes(key.Bytes())
}

func keyToStringFromBytes(keyBytes []byte) string {
	if keyBytes == nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(keyBytes)
}

func getValidatorKey(chainNodeUrl string) (crypto.PubKey, error) {
	// Get validator key through RPC
	client, err := cosmosclient.NewRpcClient(chainNodeUrl)
	if err != nil {
		return nil, fmt.Errorf("failed to create tendermint RPC client: %w", err)
	}

	// Get validator info
	result, err := client.Status(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to get status from tendermint RPC client: %w", err)
	}

	validatorKey := result.ValidatorInfo.PubKey
	return validatorKey, nil
}
