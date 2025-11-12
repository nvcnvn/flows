package flows

import (
	"context"

	"github.com/google/uuid"
)

// contextKey is a custom type for context keys to avoid collisions.
type contextKey string

const (
	tenantIDKey contextKey = "flows_tenant_id"
)

// WithTenantID returns a context with the tenant ID set.
func WithTenantID(ctx context.Context, tenantID uuid.UUID) context.Context {
	return context.WithValue(ctx, tenantIDKey, tenantID)
}

// GetTenantID retrieves the tenant ID from the context.
func GetTenantID(ctx context.Context) (uuid.UUID, bool) {
	tenantID, ok := ctx.Value(tenantIDKey).(uuid.UUID)
	return tenantID, ok
}

// MustGetTenantID retrieves the tenant ID from the context or panics.
func MustGetTenantID(ctx context.Context) uuid.UUID {
	tenantID, ok := GetTenantID(ctx)
	if !ok {
		panic(ErrNoTenantID)
	}
	return tenantID
}

// StartOption is a function that configures workflow start behavior.
type StartOption func(*startConfig)

// startConfig holds configuration for starting workflows.
type startConfig struct {
	tx Tx
}

// WithTx configures the workflow to start within an existing transaction.
// The transaction will not be committed or rolled back by the flows system.
func WithTx(tx Tx) StartOption {
	return func(c *startConfig) {
		c.tx = tx
	}
}

// SignalOption is a function that configures signal sending behavior.
type SignalOption func(*signalConfig)

// signalConfig holds configuration for sending signals.
type signalConfig struct {
	tx Tx
}

// WithSignalTx configures the signal to be sent within an existing transaction.
func WithSignalTx(tx Tx) SignalOption {
	return func(c *signalConfig) {
		c.tx = tx
	}
}

// RerunOption is a function that configures DLQ rerun behavior.
type RerunOption func(*rerunConfig)

// rerunConfig holds configuration for rerunning from DLQ.
type rerunConfig struct {
	tx Tx
}

// WithRerunTx configures the rerun to execute within an existing transaction.
func WithRerunTx(tx Tx) RerunOption {
	return func(c *rerunConfig) {
		c.tx = tx
	}
}

// getStartConfig applies options and returns the final configuration.
func getStartConfig(opts []StartOption) *startConfig {
	cfg := &startConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

// getSignalConfig applies options and returns the final configuration.
func getSignalConfig(opts []SignalOption) *signalConfig {
	cfg := &signalConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

// getRerunConfig applies options and returns the final configuration.
func getRerunConfig(opts []RerunOption) *rerunConfig {
	cfg := &rerunConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

// QueryOption is a function that configures query behavior.
type QueryOption func(*queryConfig)

// queryConfig holds configuration for querying workflows.
type queryConfig struct {
	tx Tx
}

// WithQueryTx configures the query to execute within an existing transaction.
func WithQueryTx(tx Tx) QueryOption {
	return func(c *queryConfig) {
		c.tx = tx
	}
}

// getQueryConfig applies options and returns the final configuration.
func getQueryConfig(opts []QueryOption) *queryConfig {
	cfg := &queryConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}
