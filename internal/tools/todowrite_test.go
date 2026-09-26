package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"matcode/internal/engine"
)

func execTodo(t *testing.T, wd, input string) (engine.Result, error) {
	t.Helper()
	return Todo(wd).Execute(context.Background(), json.RawMessage(input))
}

// todoState reads back the persisted list; ok is false when the file is
// absent or not a JSON array.
func todoState(t *testing.T, wd string) ([]todo, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(wd, "todos.json"))
	if err != nil {
		t.Fatalf("todos.json: %v", err)
	}
	var list []todo
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, false
	}
	return list, true
}

// TestTodoSavesState: content/status/active_form round-trip through
// todos.json, and the reply counts what was saved.
func TestTodoSavesState(t *testing.T) {
	wd := t.TempDir()
	res, err := execTodo(t, wd, `{"todos":[
		{"content":"first","status":"pending"},
		{"content":"second","status":"in_progress","active_form":"working on second"},
		{"content":"third","status":"completed"}
	]}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "saved 3 todos" {
		t.Errorf("text = %q", res.Text)
	}
	list, ok := todoState(t, wd)
	if !ok || len(list) != 3 {
		t.Fatalf("state = %v (json=%v), want 3 entries", list, ok)
	}
	if list[0].Content != "first" || list[0].Status != "pending" {
		t.Errorf("entry 0 = %+v", list[0])
	}
	if list[1].ActiveForm != "working on second" {
		t.Errorf("entry 1 = %+v", list[1])
	}
	if list[2].Status != "completed" {
		t.Errorf("entry 2 = %+v", list[2])
	}
	b, err := os.ReadFile(filepath.Join(wd, "todos.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(b), "\n") {
		t.Error("todos.json must end with a newline")
	}
}

// TestTodoReplacesState: each write is the whole list — no merge with what
// was stored before.
func TestTodoReplacesState(t *testing.T) {
	wd := t.TempDir()
	if _, err := execTodo(t, wd, `{"todos":[{"content":"old one","status":"pending"},{"content":"old two","status":"pending"}]}`); err != nil {
		t.Fatal(err)
	}
	res, err := execTodo(t, wd, `{"todos":[{"content":"only","status":"in_progress"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "saved 1 todos" {
		t.Errorf("text = %q", res.Text)
	}
	list, ok := todoState(t, wd)
	if !ok || len(list) != 1 || list[0].Content != "only" {
		t.Fatalf("state = %v, want exactly the new list", list)
	}
}

// TestTodoEmptyInput: an empty or missing list still answers and writes —
// current behaviour is a 200-style success, not an error.
func TestTodoEmptyInput(t *testing.T) {
	wd := t.TempDir()
	res, err := execTodo(t, wd, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "saved 0 todos" {
		t.Errorf("text = %q, want saved 0 todos", res.Text)
	}
	b, err := os.ReadFile(filepath.Join(wd, "todos.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "null\n" {
		t.Errorf("file = %q, want the marshalled nil list", b)
	}
	if list, ok := todoState(t, wd); !ok || len(list) != 0 {
		t.Errorf("state = %v (json=%v), want an empty list", list, ok)
	}

	wd2 := t.TempDir()
	if _, err := execTodo(t, wd2, `{"todos":[]}`); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(wd2, "todos.json")); string(b) != "[]\n" {
		t.Errorf("file = %q, want []", b)
	}
}

// TestTodoInvalidInput: malformed JSON fails before touching the file.
func TestTodoInvalidInput(t *testing.T) {
	wd := t.TempDir()
	if _, err := execTodo(t, wd, `{"todos":[`); err == nil {
		t.Fatal("malformed input must error")
	}
	if _, err := os.Stat(filepath.Join(wd, "todos.json")); !os.IsNotExist(err) {
		t.Error("a failed parse must not write state")
	}
}

// TestTodoUnwritableTarget: the persistence error is reported.
func TestTodoUnwritableTarget(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does", "not", "exist")
	if _, err := execTodo(t, missing, `{"todos":[{"content":"x","status":"pending"}]}`); err == nil {
		t.Fatal("writing into a missing dir must error")
	}
}

// TestTodoRegistration: schema requires todos; each entry requires content.
func TestTodoRegistration(t *testing.T) {
	tool := Todo(t.TempDir())
	if tool.ID != "todowrite" {
		t.Errorf("id = %q", tool.ID)
	}
	req, _ := tool.Schema["required"].([]string)
	if len(req) != 1 || req[0] != "todos" {
		t.Errorf("required = %v", tool.Schema["required"])
	}
	items, _ := tool.Schema["properties"].(map[string]any)["todos"].(map[string]any)["items"].(map[string]any)
	ireq, _ := items["required"].([]string)
	if len(ireq) != 1 || ireq[0] != "content" {
		t.Errorf("item required = %v", items["required"])
	}
}
