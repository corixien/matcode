package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Row 45: GET /api/mcp must expose env *names* and a header *count* — never
// a literal header value or env value from a hand-written mcp.json.
func TestMCPRedaction(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer fake.Close()
	srv, cwd := newTestServer(t, fake)
	h := srv.Handler()

	const (
		secretHeader = "sk- literal-bearer-DO-NOT-ECHO"
		secretEnvVal = "env-value-DO-NOT-ECHO"
	)
	mcpJSON, err := json.Marshal(map[string]any{
		"mcp": map[string]any{
			"leaky": map[string]any{
				"type":    "local",
				"command": []string{"npx", "-y", "leaky"},
				"headers": map[string]string{"Authorization": secretHeader},
				"environment": map[string]string{
					"TOKEN": secretEnvVal,
				},
				"enabled": true,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".mtc", "mcp.json"), mcpJSON, 0o644); err != nil {
		t.Fatal(err)
	}

	code, body := get(t, h, "/api/mcp")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	raw, _ := json.Marshal(body)
	resp := string(raw)

	if strings.Contains(resp, secretHeader) {
		t.Errorf("literal header value leaked in /api/mcp response")
	}
	if strings.Contains(resp, secretEnvVal) {
		t.Errorf("env value leaked in /api/mcp response")
	}
	if !strings.Contains(resp, "TOKEN") {
		t.Errorf("env name missing from /api/mcp response")
	}

	mcpList, _ := body["mcp"].([]any)
	if len(mcpList) != 1 {
		t.Fatalf("mcp list len = %d, want 1", len(mcpList))
	}
	entry, _ := mcpList[0].(map[string]any)
	if got, _ := entry["headers"].(float64); int(got) != 1 {
		t.Errorf("headers = %v, want count 1", entry["headers"])
	}
	envList, _ := entry["env"].([]any)
	if len(envList) != 1 {
		t.Errorf("env list = %v, want [TOKEN]", envList)
	}
}
