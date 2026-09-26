package permissions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestExternalDirectory(t *testing.T) {
	wd := t.TempDir()
	s := &Set{Default: Allow, WorkDir: wd, Rules: []Rule{
		{Actions: []string{ExternalDirectory}, Pattern: "/etc/*", Decision: Deny},
		{Actions: []string{ExternalDirectory}, Pattern: "/tmp/*", Decision: Ask},
	}}
	if got := s.Eval("read", raw(`{"path":"/etc/passwd"}`)); got != Deny {
		t.Errorf("/etc/passwd = %q, want deny", got)
	}
	if got := s.Eval("read", raw(`{"path":"/tmp/x"}`)); got != Ask {
		t.Errorf("/tmp/x = %q, want ask", got)
	}
	if got := s.Eval("read", raw(`{"path":"sub/file"}`)); got != Allow {
		t.Errorf("inside workdir = %q, want allow (no external rule matches)", got)
	}
	// A relative path that escapes the workdir counts as external.
	if got := s.Eval("read", raw(`{"path":"../outside"}`)); got != Ask {
		t.Errorf("../outside = %q, want ask", got)
	}
}

func TestHomeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home")
	}
	s := &Set{Default: Allow, Rules: []Rule{
		{Actions: []string{"read"}, Pattern: filepath.Join(home, "private/*"), Decision: Deny},
	}}
	if got := s.Eval("read", raw(`{"path":"~/private/a"}`)); got != Deny {
		t.Errorf("~/private/a = %q, want deny", got)
	}
	if got := s.Eval("read", raw(`{"path":"$HOME/private/b"}`)); got != Deny {
		t.Errorf("$HOME/private/b = %q, want deny", got)
	}
}

func TestMultiResourceStrictestWins(t *testing.T) {
	s := &Set{Default: Allow, Rules: []Rule{
		{Actions: []string{"write"}, Pattern: "/safe/*", Decision: Allow},
		{Actions: []string{"write"}, Pattern: "/bad/*", Decision: Deny},
	}}
	// Any deny beats any allow across multiple resources.
	if got := s.Eval("write", raw(`{"paths":["/safe/a","/bad/b"]}`)); got != Deny {
		t.Errorf("mixed paths = %q, want deny", got)
	}
	// Any ask beats any allow.
	s.Rules = []Rule{
		{Actions: []string{"write"}, Pattern: "/safe/*", Decision: Allow},
		{Actions: []string{"write"}, Pattern: "/askme/*", Decision: Ask},
	}
	if got := s.Eval("write", raw(`{"paths":["/safe/a","/askme/b"]}`)); got != Ask {
		t.Errorf("mixed paths = %q, want ask", got)
	}
	// Deny beats ask.
	s.Rules = []Rule{
		{Actions: []string{"write"}, Pattern: "/askme/*", Decision: Ask},
		{Actions: []string{"write"}, Pattern: "/bad/*", Decision: Deny},
	}
	if got := s.Eval("write", raw(`{"paths":["/askme/a","/bad/b"]}`)); got != Deny {
		t.Errorf("deny+ask = %q, want deny", got)
	}
}

func TestApprovedBeatsAskButNotDeny(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "permissions.toml")
	if err := os.WriteFile(path, []byte("default = \"allow\"\n\n[[rule]]\naction = [\"bash\"]\npattern = \"rm *\"\ndecision = \"deny\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Set{Default: Allow, Path: path, Rules: []Rule{
		{Actions: []string{"bash"}, Pattern: "deploy*", Decision: Ask},
		{Actions: []string{"bash"}, Pattern: "rm *", Decision: Deny},
	}}
	in := raw(`{"command":"deploy prod"}`)
	if got := s.Eval("bash", in); got != Ask {
		t.Fatalf("deploy = %q, want ask", got)
	}
	if err := s.Approve("bash", in, "once"); err != nil {
		t.Fatalf("approve once: %v", err)
	}
	if got := s.Eval("bash", in); got != Ask {
		t.Errorf("once must not persist: %q", got)
	}
	if err := s.Approve("bash", in, "always"); err != nil {
		t.Fatalf("approve always: %v", err)
	}
	if got := s.Eval("bash", in); got != Allow {
		t.Errorf("after always = %q, want allow (approved beats ask)", got)
	}
	// Deny can never be approved away.
	denyIn := raw(`{"command":"rm -rf /"}`)
	if err := s.Approve("bash", denyIn, "always"); err == nil {
		t.Error("approving a deny must fail")
	}
	// Persisted file must reload as an approved allow.
	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	reloaded.WorkDir = t.TempDir()
	if got := reloaded.Eval("bash", in); got != Allow {
		t.Errorf("reloaded deploy = %q, want allow", got)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "approved = true") {
		t.Errorf("persisted file lacks approved flag:\n%s", b)
	}
	// Deny rule must survive in the file (append never rewrites it).
	if !strings.Contains(string(b), "deny") {
		t.Errorf("deny rule lost:\n%s", b)
	}
}

func TestApproveWithoutPathErrors(t *testing.T) {
	s := &Set{Default: Ask}
	if err := s.Approve("bash", raw(`{"command":"ls"}`), "always"); err == nil {
		t.Error("always without a permissions.toml path must error")
	}
	if err := s.Approve("bash", raw(`{"command":"ls"}`), "once"); err != nil {
		t.Errorf("once without path: %v", err)
	}
}

func TestGlobEscapesAndWildcards(t *testing.T) {
	if !glob(`deploy *`, "deploy everything") {
		t.Error("* should span spaces")
	}
	if glob(escapeGlob("a*b"), "axb") {
		t.Error("escaped * must match literally")
	}
	if !glob(escapeGlob("a*b"), "a*b") {
		t.Error("escaped * must match the literal text")
	}
}

func TestDefaultFallback(t *testing.T) {
	s := &Set{Default: Ask}
	if got := s.Eval("read", raw(`{"path":"x"}`)); got != Ask {
		t.Errorf("miss = %q, want default ask", got)
	}
	s2 := &Set{Default: Deny, Rules: []Rule{{Actions: []string{"read"}, Pattern: "ok*", Decision: Allow}}}
	if got := s2.Eval("read", raw(`{"path":"ok"}`)); got != Allow {
		t.Errorf("match = %q, want allow", got)
	}
	if got := s2.Eval("bash", raw(`{"command":"no"}`)); got != Deny {
		t.Errorf("other action = %q, want default deny", got)
	}
}

// TestSkillResource pins the skill action: the resource is the skill ID, so
// one rule can hide a whole namespace from the model (OpenCode parity).
func TestSkillResource(t *testing.T) {
	s := &Set{Default: Allow, Rules: []Rule{
		{Actions: []string{"skill"}, Pattern: "internal-*", Decision: Deny},
		{Actions: []string{"skill"}, Pattern: "experimental-*", Decision: Ask},
	}}
	if got := s.Eval("skill", raw(`{"id":"internal-docs"}`)); got != Deny {
		t.Errorf("internal-docs = %q, want deny", got)
	}
	if got := s.Eval("skill", raw(`{"id":"experimental-x"}`)); got != Ask {
		t.Errorf("experimental-x = %q, want ask", got)
	}
	if got := s.Eval("skill", raw(`{"id":"git-release"}`)); got != Allow {
		t.Errorf("git-release = %q, want default allow", got)
	}
	// The id field must not be mistaken for a path resource.
	if got := s.Eval("skill", raw(`{"id":"internal-docs"}`)); got == Allow {
		t.Error("id leaked past the rule as a non-resource")
	}
}
