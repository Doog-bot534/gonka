package mlnodeclient

import (
	"context"
	"net/url"

	"decentralized-api/utils"
)

// PoC v2 offchain init endpoint.
// Note: the base URL (including any "/api" prefix) comes from broker Node.PoCUrlWithVersion(...).
const PoCV2InitGeneratePath = "/init/generate"

// PoCParamsModelV2 mirrors the mlnode PoC v2 params model schema.
type PoCParamsModelV2 struct {
	Model  string `json:"model"`
	SeqLen int    `json:"seq_len"`
	KDim   int    `json:"k_dim"`
}

// PoCInitGenerateRequestV2 is the JSON body for mlnode `POST /init/generate` for PoC v2.
type PoCInitGenerateRequestV2 struct {
	BlockHash   string `json:"block_hash"`
	BlockHeight int64  `json:"block_height"`
	PublicKey   string `json:"public_key"`

	NodeID    uint64 `json:"node_id"`
	NodeCount int    `json:"node_count"`

	GroupID int `json:"group_id"`
	NGroups int `json:"n_groups"`

	BatchSize int              `json:"batch_size"`
	Params    PoCParamsModelV2 `json:"params"`

	// Optional callback URL.
	URL string `json:"url,omitempty"`
}

func (api *Client) InitGenerateV2(ctx context.Context, req PoCInitGenerateRequestV2) error {
	requestURL, err := url.JoinPath(api.pocUrl, PoCV2InitGeneratePath)
	if err != nil {
		return err
	}

	_, err = utils.SendPostJsonRequest(ctx, &api.client, requestURL, req)
	if err != nil {
		return err
	}

	return nil
}
