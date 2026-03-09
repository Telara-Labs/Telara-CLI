package api

import "context"

// ConfigsClient is the subset of Client used by config commands.
// Implemented by *Client; also implemented by mocks in tests.
type ConfigsClient interface {
	ListConfigs(ctx context.Context) (*ListConfigsResponse, error)
	GetConfig(ctx context.Context, nameOrID string) (*ConfigDetail, error)
	GenerateKey(ctx context.Context, configID string, req GenerateKeyRequest) (*GenerateKeyResponse, error)
	ListKeys(ctx context.Context, configID string) (*ListKeysResponse, error)
	RevokeKey(ctx context.Context, keyID string, configID string) error
}
