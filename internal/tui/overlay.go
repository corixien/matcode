package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"matcode/internal/tui/theme"
)

// overlay is one modal layer over the chat route (spec §10 rows 22, 24,
// 25, 28, 29): a filtered item list, a question dialog, or an approval
// request. Every overlay shares select/filter/close semantics; what
// picking means depends on kind (see App.overlayPick).
type overlay struct {
	kind    string // palette | slash | mention | recents | sessions | model | agent | theme | undo | redo | help | btw | ask | provider | key
	title   string
	query   string
	items   []string
	sel     int
	listOff int
	theme   theme.Theme

	// keyProvider names the provider a kind=="key" dialog will store
	// the typed credential under (set by App.openProviderKey).
	keyProvider string

	// ask (row 29): the engine's approval request.
	askAction string
	askInput  json.RawMessage
	askReply  chan askAnswer

	// btw (row 25): side question and its answer.
	btwAnswer  string
	btwPending bool

	// tree (row 35): the working-tree browser. Rows and dir flags are
	// rebuilt on every expand/collapse; items mirror their labels.
	treeRoot     string
	treeExpanded map[string]bool
	tree         []treeRow
	treeDirs     map[string]bool

	// diff (row 35): a scrollable unified diff.
	diffLines []string
	diffOff   int
}

// newOverlay builds a list overlay in the given palette.
func newOverlay(kind, title string, items []string, t theme.Theme) *overlay {
	return &overlay{kind: kind, title: title, items: items, theme: t}
}

// style is the overlay frame style.
func (o *overlay) style() lipgloss.Style { return lipgloss.NewStyle() }

// fg maps a palette color to a style.
func (o *overlay) fg(color string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color))
}

// filtered returns items matching the query, fuzzy-ranked: exact
// matches beat prefix matches beat substring beats subsequence; inside
// a group shorter items rank first and ties keep list order. "cpt"
// therefore surfaces CppParser over every a-c-p-t scatter, and
// "snappy" still narrows a file list by subsequence alone. A mention
// query may carry a "#start-end" line range: it rides along for
// display but must not take part in matching, or no path would ever
// match.
func (o *overlay) filtered() []string {
	if o.query == "" {
		return o.items
	}
	q := o.query
	if o.kind == "mention" {
		if i := strings.IndexByte(q, '#'); i >= 0 {
			q = q[:i]
		}
	}
	q = strings.ToLower(q)
	if q == "" {
		return o.items
	}
	type hit struct {
		it     string
		rank   int
		length int
		pos    int
	}
	var hits []hit
	for _, it := range o.items {
		lower := strings.ToLower(it)
		rank := -1
		pos := -1
		switch {
		case lower == q:
			rank = 0
		case strings.HasPrefix(lower, q):
			rank = 1
		default:
			if i := strings.Index(lower, q); i >= 0 {
				rank = 2
				pos = i
			} else if fuzzy(lower, q) {
				rank = 3
			}
		}
		if rank >= 0 {
			hits = append(hits, hit{it: it, rank: rank, length: len([]rune(lower)), pos: pos})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].rank != hits[j].rank {
			return hits[i].rank < hits[j].rank
		}
		if hits[i].length != hits[j].length {
			return hits[i].length < hits[j].length
		}
		if hits[i].pos != hits[j].pos {
			return hits[i].pos < hits[j].pos
		}
		return false
	})
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.it)
	}
	return out
}

// clamp keeps sel inside the filtered list.
func (o *overlay) clamp() {
	n := len(o.filtered())
	if n == 0 {
		o.sel = 0
		return
	}
	if o.sel >= n {
		o.sel = n - 1
	}
	if o.sel < 0 {
		o.sel = 0
	}
}

// move scrolls the selection by delta.
func (o *overlay) move(delta int) { o.sel += delta; o.clamp() }

// chosen returns the selected item ("" when the list is empty).
func (o *overlay) chosen() string {
	f := o.filtered()
	if len(f) == 0 || o.sel >= len(f) {
		return ""
	}
	return f[o.sel]
}

// insert appends typed text to the filter query.
func (o *overlay) insert(s string) { o.query += s; o.sel = 0; o.clamp() }

// backspace drops one query rune.
func (o *overlay) backspace() {
	if r := []rune(o.query); len(r) > 0 {
		o.query = string(r[:len(r)-1])
	}
	o.sel = 0
	o.clamp()
}

// view renders title, body, scrolling list, and hint line. optRoom is
// how many option rows the frame has below the query line — the space
// up to the prompt field — and only applies to anchored list kinds;
// a value <= 0 derives it from height.
func (o *overlay) view(height, width, optRoom int) string {
	var lines []string
	lines = append(lines, o.titleRow(width))
	switch {
	case o.kind == "ask":
		lines = append(lines, o.askBody(width)...)
	case o.kind == "diff", o.kind == "mcplog":
		lines = append(lines, o.diffBody(width, height)...)
	case o.kind == "btw" && o.btwAnswer != "":
		lines = append(lines, o.fg(o.theme.Colors.Muted).Render("? "+o.query))
		lines = append(lines, wrapAnsi(o.btwAnswer, width-2)...)
	default:
		// The typed query always sits centred: it is the line being
		// edited, so it belongs in the middle of the frame rather
		// than wherever the box happened to land.
		lines = append(lines,
			lipgloss.PlaceHorizontal(width, lipgloss.Center, o.queryLine()))
		f := o.filtered()
		room := optRoom
		if !o.anchored() || room <= 0 {
			room = height - len(lines) - 1
		}
		if room < 3 {
			room = 3
		}
		if o.sel < o.listOff {
			o.listOff = o.sel
		}
		if o.sel >= o.listOff+room {
			o.listOff = o.sel - room + 1
		}
		for i := o.listOff; i < len(f) && i < o.listOff+room; i++ {
			row := "  " + f[i]
			if i == o.sel {
				row = o.fg(o.theme.Colors.Background).
					Background(lipgloss.Color(o.theme.Colors.Primary)).
					Render("▸ " + f[i])
			}
			lines = append(lines, truncate(row, width))
		}
	}
	// An anchored list carries its hint on the title row: the options
	// run down to the prompt field, so a bottom hint would be behind it.
	if !o.anchored() {
		lines = append(lines, o.fg(o.theme.Colors.Muted).Render(o.hint()))
	}
	return strings.Join(lines, "\n")
}

// anchored reports whether this overlay is a config menu: the box is
// pinned so the query line lands on the frame's middle row and the
// options flow downward from it, ending behind the prompt field. Tool
// dialogs (ask/diff/btw/tree) keep the old fully centred box.
func (o *overlay) anchored() bool {
	switch o.kind {
	case "ask", "diff", "mcplog", "btw", "tree":
		return false
	}
	return true
}

// titleRow is the box's first content row: the title on the left and,
// for anchored menus, the hint pushed to the right edge so it stays
// visible above the option list.
func (o *overlay) titleRow(width int) string {
	t := truncate(o.title, width)
	if !o.anchored() {
		return o.style().Bold(true).Render(t)
	}
	hint := o.hint()
	if avail := width - lipgloss.Width(hint) - 2; avail >= 1 {
		t = truncate(t, avail)
	} else {
		return o.style().Bold(true).Render(t)
	}
	gap := width - lipgloss.Width(t) - lipgloss.Width(hint)
	if gap < 1 {
		gap = 1
	}
	return o.style().Bold(true).Render(t) +
		strings.Repeat(" ", gap) +
		o.fg(o.theme.Colors.Muted).Render(hint)
}

// queryLine is the filter prompt shown above the list.
func (o *overlay) queryLine() string {
	switch o.kind {
	case "mention":
		return "@" + o.query
	case "key":
		// Masked: the key must never be rendered back to the screen.
		return "key " + o.keyProvider + " " + strings.Repeat("•", len([]rune(o.query)))
	case "btw":
		if o.btwPending {
			return "? " + o.query + " …"
		}
		return "? " + o.query
	case "provider", "model":
		return o.query
	}
	return "/" + o.query
}

// hint is the bottom help line for this overlay kind.
func (o *overlay) hint() string {
	switch o.kind {
	case "ask":
		return "y once · a always · n deny · esc deny"
	case "btw":
		if o.btwAnswer != "" {
			return "enter/esc close · never enters context"
		}
		return "enter ask · esc close · never enters context"
	case "key":
		return "enter save · esc cancel"
	case "tree":
		return treeHint
	case "diff", "mcplog":
		return "up/down scroll · pgup/pgdn page · esc close"
	default:
		return "enter select · up/down move · esc close"
	}
}

// askBody renders the approval request: action, input, and choice set.
func (o *overlay) askBody(width int) []string {
	action := o.askAction
	if action == "" {
		action = "tool"
	}
	lines := []string{
		o.fg(o.theme.Colors.Warning).Bold(true).Render("allow " + action + "?"),
	}
	if len(o.askInput) > 0 {
		lines = append(lines, wrapAnsi(string(o.askInput), width-2)...)
	}
	return lines
}

// --- candidates ---

// mentionQuery extracts a trailing "@token" from composer text. It
// returns the token (without '@') and the byte index where it starts,
// or ("",-1) when the text does not end inside a mention.
func mentionQuery(text string) (string, int) {
	idx := strings.LastIndex(text, "@")
	if idx < 0 {
		return "", -1
	}
	if idx > 0 && !strings.ContainsAny(string(text[idx-1]), " \t\n(") {
		return "", -1 // the @ belongs to a word (email, etc.)
	}
	tok := text[idx+1:]
	if tok == "" || strings.ContainsAny(tok, " \t\n") {
		return "", -1
	}
	return tok, idx
}

// matchFiles walks root for files matching query (substring, then fuzzy
// subsequence), returning at most max relative paths. Hidden directories,
// .git, and node_modules are skipped.
func matchFiles(root, query string, max int) []string {
	var out []string
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		name := info.Name()
		if info.IsDir() {
			if path != root && (strings.HasPrefix(name, ".") || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		if query == "" || fuzzy(strings.ToLower(rel), strings.ToLower(query)) {
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// fuzzy reports whether every rune of q appears in s, in order.
func fuzzy(s, q string) bool {
	if q == "" {
		return true
	}
	i := 0
	for _, r := range q {
		j := strings.IndexRune(s[i:], r)
		if j < 0 {
			return false
		}
		i += j + 1
	}
	return true
}

// wrapAnsi hard-wraps text to width cells, copying escape sequences
// through untouched (they consume no cells).
func wrapAnsi(s string, width int) []string {
	if width < 8 {
		width = 8
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		var line strings.Builder
		cells := 0
		rs := []rune(para)
		for i := 0; i < len(rs); i++ {
			r := rs[i]
			if r == 0x1b {
				for i < len(rs) {
					line.WriteRune(rs[i])
					if rs[i] == 'm' {
						break
					}
					i++
				}
				continue
			}
			w := 1
			if r > 0x1100 {
				w = 2
			}
			if cells+w > width {
				out = append(out, line.String())
				line.Reset()
				cells = 0
			}
			line.WriteRune(r)
			cells += w
		}
		out = append(out, line.String())
	}
	return out
}
