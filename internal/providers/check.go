package providers

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"matcode/internal/config"
)

// CheckKey best-effort verifies a credential against the provider's
// /models endpoint before it is stored. It deliberately errs toward
// accepting: endpoints that expose a public model list (OpenRouter,
// OpenCode Zen) answer 200 for wrong keys, and a network failure must
// never lock the user out of saving a key offline. Only a definitive
// 400/401/403 rejection is reported as an error.
func CheckKey(p config.Provider, key string) error {
	if key == "" {
		return fmt.Errorf("empty key")
	}
	if p.BaseURL == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(p.BaseURL, "/")+"/models", nil)
	if err != nil {
		return nil
	}
	if p.Dialect == "anthropic" {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil // offline or unreachable: store, verify later
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%s rejected the key (HTTP %d)", p.BaseURL, resp.StatusCode)
	default:
		return nil
	}
}
