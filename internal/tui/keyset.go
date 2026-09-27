package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"matcode/internal/config"
	"matcode/internal/providers"
)

// keyCheckMsg carries the provider's verdict on a typed credential back
// onto the event loop. Only a rejected key stops it from being stored.
type keyCheckMsg struct {
	name string
	key  string
	err  error
}

// openProvider handles /provider: with "<name> <key>" arguments it
// verifies and saves at once; without them it opens the searchable
// provider menu (the old /key id is an alias of this command).
func (a *App) openProvider(args string) (tea.Model, tea.Cmd) {
	if strings.TrimSpace(args) != "" {
		a.submitProviderKey(args)
		return a, nil
	}
	a.overlay = newOverlay("provider", "providers — enter to set an api key", a.providerItems(), a.theme)
	return a, nil
}

// providerItems renders one menu row per configured provider, sorted by
// name: "<name> <env> set|missing". The first field is the id the pick
// handler parses back out of the row.
func (a *App) providerItems() []string {
	names := make([]string, 0, len(a.cfg.Providers))
	for n := range a.cfg.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	items := make([]string, 0, len(names))
	for _, n := range names {
		p := a.cfg.Providers[n]
		env := p.APIKey.Env
		if env == "" {
			env = "-"
		}
		state := "missing"
		if _, err := p.ResolveKey(); err == nil {
			state = "set"
		}
		items = append(items, fmt.Sprintf("%-14s %-24s %s", n, env, state))
	}
	return items
}

// openProviderKey switches from the provider menu to the masked key
// input for the selected row.
func (a *App) openProviderKey(item string) {
	fields := strings.Fields(item)
	if len(fields) == 0 {
		return
	}
	name := fields[0]
	p, ok := a.cfg.Providers[name]
	if !ok {
		a.status = "provider: unknown provider " + name
		return
	}
	if p.APIKey.Env == "" {
		a.status = "provider " + name + " has no api_key.env configured"
		return
	}
	o := newOverlay("key", "api key for "+name+" ("+p.APIKey.Env+", stored in .env)", nil, a.theme)
	o.keyProvider = name
	a.overlay = o
}

// submitProviderKey parses "/provider <name> <key>" (or a bare key with
// the current model's provider as fallback) and verifies it.
func (a *App) submitProviderKey(input string) {
	fallback := providerOf(a.currentModel(), a.cfg.Model)
	name, key, err := parseKeyInput(input, fallback)
	if err != nil {
		a.status = "provider: " + err.Error()
		return
	}
	a.checkProviderKey(name, key)
}

// checkProviderKey verifies the credential against the provider before
// storing it; the verdict arrives as keyCheckMsg on the event loop.
func (a *App) checkProviderKey(name, key string) {
	p, ok := a.cfg.Providers[name]
	if !ok {
		a.status = "provider: unknown provider " + name
		return
	}
	if p.APIKey.Env == "" {
		a.status = "provider " + name + " has no api_key.env configured"
		return
	}
	if key == "" {
		a.status = "provider: empty key"
		return
	}
	a.status = "checking " + name + " api key…"
	go func() {
		err := providers.CheckKey(p, key)
		a.send(keyCheckMsg{name: name, key: key, err: err})
	}()
}

// saveProviderKey stores the credential in the data dir .env (0600) and
// re-binds every tab whose model resolves to it, so no restart is needed.
func (a *App) saveProviderKey(name, key string) {
	p, ok := a.cfg.Providers[name]
	if !ok {
		a.status = "provider: unknown provider " + name
		return
	}
	if p.APIKey.Env == "" {
		a.status = "provider " + name + " has no api_key.env configured"
		return
	}
	if err := config.SetKey(a.cfg.DataDir(), p.APIKey.Env, key); err != nil {
		a.status = "provider: " + err.Error()
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
	a.status = fmt.Sprintf("saved %s → %s/.env — pick models with /model", p.APIKey.Env, a.cfg.DataDir())
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
			return "", "", fmt.Errorf("usage: /provider <provider> <key>")
		}
		return fallback, fields[0], nil
	case 2:
		return fields[0], fields[1], nil
	default:
		return "", "", fmt.Errorf("usage: /provider [provider] <key>")
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
