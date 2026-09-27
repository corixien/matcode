package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"matcode/internal/config"
)

// ListModels asks the provider's /models endpoint for every model the
// credential may use and returns their ids, in the order the provider
// reports them.
//
// This is the same request CheckKey makes, kept separate so the model
// picker can show the full catalogue behind a key instead of the
// handful of preset ids a provider entry happens to carry. Every
// OpenAI-shaped API answers {"data":[{"id":…}]} — OpenAI, OpenRouter,
// OpenCode Zen, and the compatible gateways — so one parser covers
// them; Anthropic answers the same way at /v1/models. A non-200, an
// unparsable body, or an empty list is an error: the caller then falls
// back to its static choices rather than showing an empty menu.
func ListModels(p config.Provider, key string) ([]string, error) {
	if p.BaseURL == "" {
		return nil, fmt.Errorf("no base url")
	}
	if key == "" {
		return nil, fmt.Errorf("empty key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(p.BaseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	if p.Dialect == "anthropic" {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("models: %w", err)
	}
	ids := make([]string, 0, len(payload.Data))
	seen := make(map[string]bool, len(payload.Data))
	for _, m := range payload.Data {
		if m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		ids = append(ids, m.ID)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("models: empty list")
	}
	return ids, nil
}
