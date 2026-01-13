package mlnode

import (
	"decentralized-api/broker"
	cosmos_client "decentralized-api/cosmosclient"
	"decentralized-api/internal/server/middleware"
	"decentralized-api/pocstorage"

	"github.com/labstack/echo/v4"
)

type Server struct {
	e        *echo.Echo
	recorder cosmos_client.CosmosMessageClient
	broker   *broker.Broker
	pocStore pocstorage.PoCStorage
}

// TODO breacking changes: url path, support on mlnode side
func NewServer(recorder cosmos_client.CosmosMessageClient, broker *broker.Broker, pocStore pocstorage.PoCStorage) *Server {
	e := echo.New()

	e.HTTPErrorHandler = middleware.TransparentErrorHandler

	e.Use(middleware.LoggingMiddleware)
	g := e.Group("/mlnode/v1/")
	v2 := e.Group("/mlnode/v2/")

	s := &Server{
		e:        e,
		recorder: recorder,
		broker:   broker,
		pocStore: pocStore,
	}

	// keep old paths too for backward compatibility
	g.POST("poc-batches/generated", s.postGeneratedBatches)
	e.POST("/v1/poc-batches/generated", s.postGeneratedBatches)

	g.POST("poc-batches/validated", s.postValidatedBatches)
	e.POST("/v1/poc-batches/validated", s.postValidatedBatches)

	// PoC v2 offchain callback endpoint for mlnodes (new nonce format)
	// PoC v2 artifact callback shape (vector_b64).
	v2.POST("poc-artifacts/generated", s.postGeneratedArtifactsV2)
	e.POST("/v2/poc-artifacts/generated", s.postGeneratedArtifactsV2)
	return s
}

func (s *Server) Start(addr string) {
	go s.e.Start(addr)
}
