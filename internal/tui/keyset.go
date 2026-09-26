package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"matcode/internal/config"
	"matcode/internal/providers"
)

// openKey handles /key: with arguments it saves immediately, without them
// it opens the masked input dialog. The key is written to the data dir
// .env (0600) and the running engines are rebound in place, so no restart
// is needed (row: /key).
func (a *App) openKey(args string) (tea.Model, tea.Cmd) {
	if args != "" {
		a.saveKey(args)
		return a, nil
	}
	target := providerOf(a.currentModel(), a.cfg.Model)
	a.overlay = newOverlay("key", "api key for "+target+" (stored in .env)", nil, a.theme)
	return a, nil
}

// saveKey parses "<key>" or "<provider> <key>", persists the credential,
// and re-binds every tab whose model resolves to it.
func (a *App) saveKey(input string) {
	fallback := providerOf(a.currentModel(), a.cfg.Model)
	name, key, err := parseKeyInput(input, fallback)
	if err != nil {
		a.status = "key: " + err.Error()
		return
	}
	p, ok := a.cfg.Providers[name]
	if !ok {
		a.status = "key: unknown provider " + name
		return
	}
	if p.APIKey.Env == "" {
		a.status = "key: provider " + name + " has no api_key.env configured"
		return
	}
	if err := config.SetKey(a.cfg.DataDir(), p.APIKey.Env, key); err != nil {
		a.status = "key: " + err.Error()
		return
	}
	// Rebind live engines so the next turn uses the credential.
	for _, t := range a.tabs {
		if t == nil || t.built == nil || t.built.Engine == nil {
			continue
		}
		m := ""
		if t.sess != nil {
			m = t.sess.Meta.Model
		}
		if m == "" {
			m = a.cfg.Model
		}
		if pr, mm, err := providers.For(a.cfg, m); err == nil {
			t.built.Engine.Provider = pr
			t.built.Engine.Model = mm
			t.built.Model = mm
			t.built.ProviderErr = nil
		}
	}
	a.status = fmt.Sprintf("saved %s → %s/.env — switch models with ctrl+x m", p.APIKey.Env, a.cfg.DataDir())
}

// parseKeyInput accepts "<key>" (provider taken from the current model)
// or "<provider> <key>".
func parseKeyInput(input, fallback string) (name, key string, err error) {
	fields := strings.Fields(input)
	switch len(fields) {
	case 0:
		return "", "", fmt.Errorf("empty key")
	case 1:
		if fallback == "" {
			return "", "", fmt.Errorf("usage: /key <provider> <key>")
		}
		return fallback, fields[0], nil
	case 2:
		return fields[0], fields[1], nil
	default:
		return "", "", fmt.Errorf("usage: /key [provider] <key>")
	}
}

// providerOf returns the provider segment (everything before the first
// slash) of the first non-empty ref.
func providerOf(refs ...string) string {
	for _, ref := range refs {
		if ref == "" {
			continue
		}
		if i := strings.IndexByte(ref, '/'); i > 0 {
			return ref[:i]
		}
		return ref
	}
	return ""
}
