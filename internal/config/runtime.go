package config

import (
	"context"
	"fmt"

	"yunshu.dev/core/internal/jsonx"
)

// RuntimeWriter is the /admin/config path. It accepts only the 12-key
// whitelist in key.go and applies the V1.0 value restrictions declared in the
// same metadata table. Calls that fail validation never reach Redis or MySQL.
type RuntimeWriter struct {
	Publisher *Publisher
}

// Set validates and publishes one runtime change.
func (w *RuntimeWriter) Set(ctx context.Context, key Key, raw jsonx.RawMessage, operatorID uint64) (*PublishResult, error) {
	if w.Publisher == nil {
		return nil, fmt.Errorf("config: runtime writer requires a publisher")
	}
	if err := ValidateRuntime(key, raw); err != nil {
		return nil, err
	}
	return w.Publisher.Publish(ctx, []Change{{Key: key, Raw: raw}}, operatorID)
}
