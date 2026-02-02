package ethclient

import (
	"context"
	"fmt"
	"math/big"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/golang-jwt/jwt/v5"
)

// EngineClient wraps the Engine API RPC client
type EngineClient struct {
	rpc *rpc.Client
}

// NewEngineClient creates a new Engine API client with JWT authentication
func NewEngineClient(ctx context.Context, endpoint string, jwtSecret []byte) (*EngineClient, error) {
	transport := &jwtRoundTripper{
		underlyingTransport: http.DefaultTransport,
		jwtSecret:           jwtSecret,
	}

	httpClient := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	rpcClient, err := rpc.DialOptions(ctx, endpoint, rpc.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("dial engine endpoint: %w", err)
	}

	return &EngineClient{rpc: rpcClient}, nil
}

func (c *EngineClient) HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error) {
	var header *types.Header
	err := c.rpc.CallContext(ctx, &header, "eth_getBlockByNumber", toBlockNumArg(number), false)
	if err != nil {
		return nil, err
	}
	if header == nil {
		return nil, fmt.Errorf("block not found: %s", toBlockNumArg(number))
	}
	return header, nil
}

func (c *EngineClient) ForkchoiceUpdatedV3(ctx context.Context, state engine.ForkchoiceStateV1, attrs *engine.PayloadAttributes) (engine.ForkChoiceResponse, error) {
	var resp engine.ForkChoiceResponse
	err := c.rpc.CallContext(ctx, &resp, "engine_forkchoiceUpdatedV3", state, attrs)
	return resp, err
}

func (c *EngineClient) GetPayloadV3(ctx context.Context, payloadID engine.PayloadID) (*engine.ExecutionPayloadEnvelope, error) {
	var resp engine.ExecutionPayloadEnvelope
	err := c.rpc.CallContext(ctx, &resp, "engine_getPayloadV3", payloadID)
	return &resp, err
}

func (c *EngineClient) NewPayloadV3(ctx context.Context, payload engine.ExecutableData, versionedHashes []common.Hash, beaconRoot *common.Hash) (engine.PayloadStatusV1, error) {
	var resp engine.PayloadStatusV1
	err := c.rpc.CallContext(ctx, &resp, "engine_newPayloadV3", payload, versionedHashes, beaconRoot)
	return resp, err
}

// jwtRoundTripper adds JWT authentication to HTTP requests
type jwtRoundTripper struct {
	underlyingTransport http.RoundTripper
	jwtSecret           []byte
}

func (t *jwtRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iat": time.Now().Unix(),
	})

	tokenString, err := token.SignedString(t.jwtSecret)
	if err != nil {
		return nil, fmt.Errorf("sign JWT: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+tokenString)
	return t.underlyingTransport.RoundTrip(req)
}

func toBlockNumArg(number *big.Int) string {
	if number == nil {
		return "latest"
	}
	return hexutil.EncodeBig(number)
}
