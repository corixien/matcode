// Package config resolves matcode's configuration from its data tree.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"matcode/internal/references"
)

// EnvRef names the environment variable holding a secret. Secrets are never
// stored in a file; the file only names the variable.
type EnvRef struct {
	Env string `toml:"env"`
}

// Provider describes one upstream model service.
type Provider struct {
	Dialect      string `toml:"dialect"` // "openai" (default) or "anthropic"
	BaseURL      string `toml:"base_url"`
	APIKey       EnvRef `toml:"api_key"`
	DefaultModel string `toml:"default_model"`
	// Models lists the model ids this provider serves, first is default
	// when DefaultModel is empty (row 35: the model picker's variants).
	Models []string `toml:"models"`
}

// ResolveKey reads the provider's API key from its environment variable.
func (p Provider) ResolveKey() (string, error) {
	if p.APIKey.Env == "" {
		return "", nil
	}
	v := os.Getenv(p.APIKey.Env)
	if v == "" {
		return "", fmt.Errorf("environment variable %s is not set", p.APIKey.Env)
	}
	return v, nil
}

// file is the on-disk shape of config.toml. It holds only keys matcode reads.
type file struct {
	Model        string              `toml:"model"`
	Agent        string              `toml:"agent"`
	DefaultAgent string              `toml:"default_agent"`
	Formatter    *bool               `toml:"formatter"`
	Snapshots    *bool               `toml:"snapshots"`
	AutoCompact  *bool               `toml:"auto_compact"`
	ContextLimit int                 `toml:"context_limit"`
	APIPort      int                 `toml:"api_port"`
	Theme        string              `toml:"theme"`
	Media        *mediaFile          `toml:"media"`
	UI           *uiFile             `toml:"ui"`
	Providers    map[string]Provider `toml:"providers"`
	Refs         map[string]any      `toml:"references"`
}

// mediaFile is the [media] table: image handling for attachments.
type mediaFile struct {
	AutoResize     *bool `toml:"auto_resize"`
	MaxBase64Bytes int   `toml:"max_base64_bytes"`
	MaxFileBytes   int   `toml:"max_file_bytes"`
}

// uiFile is the [ui] table: presentation toggles (row 38).
type uiFile struct {
	ShowTokens *bool  `toml:"show_tokens"`
	Editor     string `toml:"editor"`
}

// Media is the resolved attachment/media policy.
type Media struct {
	AutoResize     bool
	MaxBase64Bytes int
	MaxFileBytes   int
}

// Config is the resolved configuration.
type Config struct {
	Model           string
	Agent           string // default agent id; "" = "build"
	Formatter       bool   // run ext-matched formatters after write/edit/patch
	Snapshots       bool   // capture per-step file snapshots for undo/redo
	AutoCompact     bool   // fold the transcript at the token threshold (§11)
	ContextLimit    int    // model context window in tokens (auto-compact budget)
	ContextLimitSet bool   // true when a config file set context_limit (catalog may override when false)
	APIPort         int    // port for the local HTTP API (mtc serve / mtc api)
	Theme           string // theme name from themes/ ("default" = built-in)
	Media           Media
	UIShowTokens    bool   // [ui] show_tokens: display token counters (row 38)
	UIEditor        string // [ui] editor: "" = auto ($VISUAL/$EDITOR/vi)
	Providers       map[string]Provider
	References      map[string]references.Ref
	GlobalDir       string
	ProjectDir      string // empty when the working directory has no .mtc tree
}

// Load reads config.toml from the global tree and the project tree, project
// winning per key, then overlays both on the built-in provider defaults.
func Load(cwd string) (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		GlobalDir:    filepath.Join(home, ".config", "mtc"),
		Providers:    defaultProviders(),
		Snapshots:    true,
		AutoCompact:  true,
		ContextLimit: 128000,
		APIPort:      8787,
		Theme:        "default",
		UIShowTokens: true,
		Media: Media{
			AutoResize:     true,
			MaxBase64Bytes: 5 << 20,
			MaxFileBytes:   20 << 20,
		},
	}
	if isDir(filepath.Join(cwd, ".mtc")) {
		cfg.ProjectDir = filepath.Join(cwd, ".mtc")
	}

	var merged file
	for _, dir := range []string{cfg.GlobalDir, cfg.ProjectDir} {
		name := filepath.Join(dir, "config.toml")
		if !isFile(name) {
			continue
		}
		var got file
		if _, err := toml.DecodeFile(name, &got); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if got.Model != "" {
			merged.Model = got.Model
		}
		if got.Agent != "" {
			merged.Agent = got.Agent
		}
		if got.DefaultAgent != "" && merged.Agent == "" {
			merged.DefaultAgent = got.DefaultAgent
		}
		if got.Formatter != nil {
			merged.Formatter = got.Formatter
		}
		if got.Snapshots != nil {
			merged.Snapshots = got.Snapshots
		}
		if got.AutoCompact != nil {
			merged.AutoCompact = got.AutoCompact
		}
		if got.ContextLimit > 0 {
			merged.ContextLimit = got.ContextLimit
		}
		if got.APIPort > 0 {
			merged.APIPort = got.APIPort
		}
		if got.Theme != "" {
			merged.Theme = got.Theme
		}
		if got.Media != nil {
			merged.Media = mergeMedia(merged.Media, got.Media)
		}
		if got.UI != nil {
			merged.UI = mergeUI(merged.UI, got.UI)
		}
		for name, p := range got.Providers {
			base := cfg.Providers[name]
			overlayProvider(&base, p)
			cfg.Providers[name] = base
		}
		if len(got.Refs) > 0 {
			// Relative reference paths resolve from the project root for
			// the project config (its config.toml lives in .mtc/), and
			// from the global config's directory for the global layer.
			base := filepath.Dir(name)
			if dir == cfg.ProjectDir {
				base = cwd
			}
			parsed, err := references.Parse(base, cfg.DataDir(), got.Refs)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			if cfg.References == nil {
				cfg.References = map[string]references.Ref{}
			}
			for alias, r := range parsed {
				cfg.References[alias] = r
			}
		}
	}
	cfg.Model = merged.Model
	if cfg.Model == "" {
		// Rice default (spec §3): a fresh install opens the TUI with a
		// working model instead of erroring on the first prompt.
		cfg.Model = DefaultModel
	}
	cfg.Agent = merged.Agent
	if cfg.Agent == "" {
		cfg.Agent = merged.DefaultAgent
	}
	cfg.Formatter = merged.Formatter != nil && *merged.Formatter
	if merged.Snapshots != nil {
		cfg.Snapshots = *merged.Snapshots
	}
	if merged.AutoCompact != nil {
		cfg.AutoCompact = *merged.AutoCompact
	}
	if merged.ContextLimit > 0 {
		cfg.ContextLimit = merged.ContextLimit
		cfg.ContextLimitSet = true
	}
	if merged.APIPort > 0 {
		cfg.APIPort = merged.APIPort
	}
	if merged.Theme != "" {
		cfg.Theme = merged.Theme
	}
	if merged.Media != nil {
		if merged.Media.AutoResize != nil {
			cfg.Media.AutoResize = *merged.Media.AutoResize
		}
		if merged.Media.MaxBase64Bytes > 0 {
			cfg.Media.MaxBase64Bytes = merged.Media.MaxBase64Bytes
		}
		if merged.Media.MaxFileBytes > 0 {
			cfg.Media.MaxFileBytes = merged.Media.MaxFileBytes
		}
	}
	if merged.UI != nil {
		if merged.UI.ShowTokens != nil {
			cfg.UIShowTokens = *merged.UI.ShowTokens
		}
		if merged.UI.Editor != "" {
			cfg.UIEditor = merged.UI.Editor
		}
	}
	return cfg, nil
}

// Provider returns a configured provider by name.
func (c *Config) Provider(name string) (Provider, bool) {
	p, ok := c.Providers[name]
	return p, ok
}

// DataDir is the effective data tree: the project .mtc if present, else global.
func (c *Config) DataDir() string {
	if c.ProjectDir != "" {
		return c.ProjectDir
	}
	return c.GlobalDir
}

// SessionsDir is where session folders live.
func (c *Config) SessionsDir() string {
	return filepath.Join(c.DataDir(), "sessions")
}

// APIBaseURL is where mtc serve listens and mtc api connects by default.
func (c *Config) APIBaseURL() string {
	port := c.APIPort
	if port <= 0 {
		port = 8787
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

// SkillDirs lists the skill source roots, lowest precedence first: global,
// then project, so a project definition replaces a global one with the same
// ID (spec §3 resolution rules).
func (c *Config) SkillDirs() []string {
	var out []string
	for _, dir := range []string{c.GlobalDir, c.ProjectDir} {
		if dir == "" {
			continue
		}
		out = append(out, filepath.Join(dir, "skills"))
	}
	return out
}

// AgentsDirs lists the agent (.md) source roots, lowest precedence first:
// global, then project, so a project file replaces a global one with the
// same ID (spec §3 resolution rules).
func (c *Config) AgentsDirs() []string {
	var out []string
	for _, dir := range []string{c.GlobalDir, c.ProjectDir} {
		if dir == "" {
			continue
		}
		out = append(out, filepath.Join(dir, "agents"))
	}
	return out
}

// CommandsDirs lists the slash-command source roots (commands/<name>.md),
// lowest precedence first: global, then project.
func (c *Config) CommandsDirs() []string {
	var out []string
	for _, dir := range []string{c.GlobalDir, c.ProjectDir} {
		if dir == "" {
			continue
		}
		out = append(out, filepath.Join(dir, "commands"))
	}
	return out
}

// ToolsDirs lists the user-tool roots (tools/<name>/tool.toml + exec.sh),
// lowest precedence first: global, then project. Project wins file-by-file.
func (c *Config) ToolsDirs() []string {
	var out []string
	for _, dir := range []string{c.GlobalDir, c.ProjectDir} {
		if dir == "" {
			continue
		}
		out = append(out, filepath.Join(dir, "tools"))
	}
	return out
}

// PluginsDirs lists the plugin roots (plugins/<name>/plugin.toml), lowest
// precedence first: global, then project. Project wins plugin-by-plugin.
func (c *Config) PluginsDirs() []string {
	var out []string
	for _, dir := range []string{c.GlobalDir, c.ProjectDir} {
		if dir == "" {
			continue
		}
		out = append(out, filepath.Join(dir, "plugins"))
	}
	return out
}

// PluginsDir returns the directory `mtc plugin new` scaffolds into
// (project first when it exists, else global), whether or not it exists.
func (c *Config) PluginsDir() string {
	for _, dir := range []string{c.ProjectDir, c.GlobalDir} {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, "plugins")
		if isDir(p) {
			return p
		}
	}
	if c.ProjectDir != "" {
		return filepath.Join(c.ProjectDir, "plugins")
	}
	if c.GlobalDir != "" {
		return filepath.Join(c.GlobalDir, "plugins")
	}
	return "plugins"
}

// ThemesDir returns the themes directory (project first, then global),
// whether or not it exists yet, so a loader can list or create it.
func (c *Config) ThemesDir() string {
	for _, dir := range []string{c.ProjectDir, c.GlobalDir} {
		if dir == "" {
			continue
		}
		if p := filepath.Join(dir, "themes"); isDir(p) {
			return p
		}
	}
	for _, dir := range []string{c.ProjectDir, c.GlobalDir} {
		if dir == "" {
			continue
		}
		return filepath.Join(dir, "themes")
	}
	return filepath.Join(os.TempDir(), "mtc-themes")
}

// Find locates a file in the data tree, project first then global.
func (c *Config) Find(rel string) (string, bool) {
	for _, dir := range []string{c.ProjectDir, c.GlobalDir} {
		if dir == "" {
			continue
		}
		name := filepath.Join(dir, rel)
		if isFile(name) {
			return name, true
		}
	}
	return "", false
}

// mergeMedia overlays src onto dst per key (nil = unset).
// DefaultModel is the rice default model (spec §3): applied when no config
// file sets `model`.
const DefaultModel = "anthropic/claude-sonnet-4-5"

func mergeMedia(dst, src *mediaFile) *mediaFile {
	out := mediaFile{}
	if dst != nil {
		out = *dst
	}
	if src.AutoResize != nil {
		out.AutoResize = src.AutoResize
	}
	if src.MaxBase64Bytes > 0 {
		out.MaxBase64Bytes = src.MaxBase64Bytes
	}
	if src.MaxFileBytes > 0 {
		out.MaxFileBytes = src.MaxFileBytes
	}
	return &out
}

func mergeUI(dst, src *uiFile) *uiFile {
	out := uiFile{}
	if dst != nil {
		out = *dst
	}
	if src.ShowTokens != nil {
		out.ShowTokens = src.ShowTokens
	}
	if src.Editor != "" {
		out.Editor = src.Editor
	}
	return &out
}

func overlayProvider(dst *Provider, src Provider) {
	if src.Dialect != "" {
		dst.Dialect = src.Dialect
	}
	if src.BaseURL != "" {
		dst.BaseURL = src.BaseURL
	}
	if src.APIKey.Env != "" {
		dst.APIKey = src.APIKey
	}
	if src.DefaultModel != "" {
		dst.DefaultModel = src.DefaultModel
	}
	if len(src.Models) > 0 {
		dst.Models = src.Models
	}
	if dst.DefaultModel == "" && len(dst.Models) > 0 {
		dst.DefaultModel = dst.Models[0]
	}
	if dst.Dialect == "" {
		dst.Dialect = "openai"
	}
}

func isFile(name string) bool {
	st, err := os.Stat(name)
	return err == nil && st.Mode().IsRegular()
}

func isDir(name string) bool {
	st, err := os.Stat(name)
	return err == nil && st.IsDir()
}
