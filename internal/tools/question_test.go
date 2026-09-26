package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"matcode/internal/engine"
)

// questionStdin points the tool at a canned answer and restores the shared
// reader afterwards — one bufio.Reader serves every call in the process.
func questionStdin(t *testing.T, answer string) {
	t.Helper()
	prev := stdin
	stdin = bufio.NewReader(strings.NewReader(answer))
	t.Cleanup(func() { stdin = prev })
}

// questionExec executes the tool while capturing the prompt it prints.
func questionExec(t *testing.T, input string) (engine.Result, string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdout
	os.Stdout = w

	var res engine.Result
	var execErr error
	func() {
		defer func() { os.Stdout = prev }()
		res, execErr = Question(t.TempDir()).Execute(context.Background(), json.RawMessage(input))
	}()
	w.Close()

	b, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	return res, string(b), execErr
}

// questionRun is questionExec for the happy path.
func questionRun(t *testing.T, input string) (engine.Result, string) {
	t.Helper()
	res, out, err := questionExec(t, input)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return res, out
}

// TestQuestionFreeFormAndPrompt: header, question and options are printed
// for the terminal user; a typed line comes back verbatim.
func TestQuestionFreeFormAndPrompt(t *testing.T) {
	questionStdin(t, "purple\n")
	res, out := questionRun(t, `{"question":"Pick a colour","header":"Colour"}`)
	if res.Text != "purple" {
		t.Errorf("answer = %q, want the typed text", res.Text)
	}
	for _, want := range []string{"[[ Colour ]]\n", "? Pick a colour\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt missing %q, got %q", want, out)
		}
	}

	questionStdin(t, "2\n")
	res, out = questionRun(t,
		`{"question":"Pick","options":["red","green","blue"]}`)
	if res.Text != "green" {
		t.Errorf("index 2 = %q, want green", res.Text)
	}
	if !strings.Contains(out, "  1) red\n  2) green\n  3) blue\n") {
		t.Errorf("options were not listed: %q", out)
	}
	if strings.Contains(out, "[[") {
		t.Errorf("an empty header must print nothing: %q", out)
	}
}

// TestQuestionSingleSelect: in-range indices map to the option text,
// anything else passes through as typed.
func TestQuestionSingleSelect(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"1\n", "red"},
		{" 3\n", "blue"},
		{"9\n", "9"},
		{"blue\n", "blue"},
		{"first\n", "first"},
	} {
		questionStdin(t, tc.in)
		res, _ := questionRun(t, `{"question":"Pick","options":["red","green","blue"]}`)
		if res.Text != tc.want {
			t.Errorf("input %q -> %q, want %q", tc.in, res.Text, tc.want)
		}
	}
}

// TestQuestionMultiple: comma/space/semicolon separated indices resolve to
// option texts; any unparseable token passes the whole answer through.
func TestQuestionMultiple(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"1,3\n", "red, blue"},
		{"1 3\n", "red, blue"},
		{"2;1\n", "green, red"},
		{"blue\n", "blue"},
		{"1,bad\n", "1,bad"},
		{"9\n", "9"},
		{",\n", ","},
	} {
		questionStdin(t, tc.in)
		res, _ := questionRun(t, `{"question":"Pick","options":["red","green","blue"],"multiple":true}`)
		if res.Text != tc.want {
			t.Errorf("input %q -> %q, want %q", tc.in, res.Text, tc.want)
		}
	}
}

// TestQuestionAnswerFailures: closed stdin and blank lines fail loudly —
// the tool never guesses an answer.
func TestQuestionAnswerFailures(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"closed", "", "no answer available (stdin closed)"},
		{"blank", "\n", "empty answer"},
		{"whitespace", "   \n", "empty answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			questionStdin(t, tc.in)
			_, _, err := questionExec(t, `{"question":"q"}`)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestQuestionEOFWithPartialLine: a final line without a newline still
// counts as an answer.
func TestQuestionEOFWithPartialLine(t *testing.T) {
	questionStdin(t, "half an answer")
	res, _ := questionRun(t, `{"question":"q"}`)
	if res.Text != "half an answer" {
		t.Errorf("answer = %q", res.Text)
	}
}

func TestQuestionInvalidJSON(t *testing.T) {
	questionStdin(t, "x\n")
	if _, _, err := questionExec(t, `{"question":`); err == nil {
		t.Fatal("malformed input must error")
	}
}
