package resolve

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/thec1oud/dpg/internal/drift"
	"github.com/thec1oud/dpg/internal/pipeline"
	"github.com/thec1oud/dpg/internal/scanutil"
	"github.com/thec1oud/dpg/internal/snapshot"
	"github.com/thec1oud/dpg/internal/srcindex"
)

// textEdit replaces [start, end) of a file's normalized text with text.
type textEdit struct {
	path       string
	start, end int
	text       string
	decision   Decision
}

// snapTask updates one snapshot entry from the edited source: the whole
// entry (property == ""), one property, or removes it (remove).
type snapTask struct {
	key, property string
	remove        bool
}

// adoptAll plans adoptions: text edits to the source, then the snapshot
// entries recomputed from the edited source (compiled in a scratch copy, so
// a broken edit is caught before anything is written).
func (pl *planner) adoptAll(targets map[string]*Target, decisions []Decision, deps Deps) error {
	byTarget := map[*Target][]Decision{}
	var order []*Target
	for _, d := range decisions {
		t := targets[d.Target]
		if _, seen := byTarget[t]; !seen {
			order = append(order, t)
		}
		byTarget[t] = append(byTarget[t], d)
	}
	for _, t := range order {
		var edits []textEdit
		var tasks []snapTask
		for _, d := range byTarget[t] {
			it, _ := findItem(t.Report, d.ItemID)
			e, task, err := pl.adoptOne(t, it, d, deps)
			if err != nil {
				pl.problem(d, "%v", err)
				continue
			}
			edits = append(edits, e...)
			tasks = append(tasks, task...)
		}
		if err := pl.applyEdits(edits); err != nil {
			return err
		}
		if len(tasks) == 0 {
			continue
		}
		src, err := pl.compileEdited(t, deps)
		if err != nil {
			for _, d := range byTarget[t] {
				pl.problem(d, "the adopted source doesn't compile: %v", err)
			}
			return nil
		}
		snap := pl.snapshot(t)
		for _, task := range tasks {
			if err := applySnapTask(snap, src, task); err != nil {
				return err
			}
		}
	}
	return nil
}

// adoptOne plans adopting one item.
func (pl *planner) adoptOne(t *Target, it drift.Item, d Decision, deps Deps) ([]textEdit, []snapTask, error) {
	switch {
	case it.Group == drift.Converged:
		// The source already has the database's value: only the snapshot
		// is out of date.
		return nil, []snapTask{{key: it.Object, property: it.Property}}, nil
	case it.Property == "" && it.Change == "extra":
		return pl.adoptNewObject(t, it, d, deps)
	case it.Property == "" && it.Change == "missing":
		return pl.adoptMissingObject(t, it, d)
	case strings.Contains(it.Property, "grant:"):
		return pl.adoptGrant(t, it, d)
	default:
		return pl.adoptProperty(t, it, d, deps)
	}
}

// adoptNewObject declares a live object the source doesn't: its rendering
// is appended to the file such an object belongs in.
func (pl *planner) adoptNewObject(t *Target, it drift.Item, d Decision, deps Deps) ([]textEdit, []snapTask, error) {
	live := findObject(t.Live, it.Object)
	if live == nil {
		return nil, nil, fmt.Errorf("not in the database")
	}
	text := strings.TrimLeft(deps.Render(live), "\n")
	path := t.NewFile(it.Kind, it.Object)
	fc, err := pl.file(path)
	if err != nil {
		return nil, nil, err
	}
	norm := scanutil.NormalizeSource(fc.After)
	sep := ""
	if len(norm) > 0 {
		sep = "\n"
		if !bytes.HasSuffix(norm, []byte("\n")) {
			sep = "\n\n"
		}
	}
	return []textEdit{{path: path, start: len(norm), end: len(norm), text: sep + text, decision: d}},
		[]snapTask{{key: it.Object}}, nil
}

// adoptMissingObject removes every declaration of an object the database
// no longer has.
func (pl *planner) adoptMissingObject(t *Target, it drift.Item, d Decision) ([]textEdit, []snapTask, error) {
	decls := t.Index.Decls[it.Object]
	var edits []textEdit
	for _, dc := range decls {
		if dc.Decl.Macro != "" {
			return nil, nil, fmt.Errorf("declared by macro %s; edit it by hand", dc.Decl.Macro)
		}
		fc, err := pl.file(dc.Decl.File)
		if err != nil {
			return nil, nil, err
		}
		start, end := wholeLines(scanutil.NormalizeSource(fc.Before), dc.Decl.Start, dc.Decl.End)
		edits = append(edits, textEdit{path: dc.Decl.File, start: start, end: end, decision: d})
	}
	return edits, []snapTask{{key: it.Object, remove: true}}, nil
}

// adoptProperty replaces the text declaring a property with the database's
// value, cut from the live object's rendering; a sub-object the database no
// longer has is removed.
func (pl *planner) adoptProperty(t *Target, it drift.Item, d Decision, deps Deps) ([]textEdit, []snapTask, error) {
	key := srcindex.PropertyKey(it.Property)
	srcSpans := spansWithOrigin(t.Index, it.Object, key)
	for _, s := range srcSpans {
		if s.span.Macro != "" {
			return nil, nil, fmt.Errorf("declared by macro %s", s.span.Macro)
		}
	}
	task := []snapTask{{key: it.Object, property: it.Property}}

	if it.Change == "missing" {
		if len(srcSpans) == 0 {
			return nil, nil, fmt.Errorf("no source text declares %s", key)
		}
		var edits []textEdit
		for _, s := range srcSpans {
			fc, err := pl.file(s.span.File)
			if err != nil {
				return nil, nil, err
			}
			start, end := removalRange(scanutil.NormalizeSource(fc.Before), s.span, s.origin)
			edits = append(edits, textEdit{path: s.span.File, start: start, end: end, decision: d})
		}
		return edits, task, nil
	}

	live := findObject(t.Live, it.Object)
	if live == nil {
		return nil, nil, fmt.Errorf("not in the database")
	}
	rendered, err := pl.renderedSpans(t, it, live, key, deps)
	if err != nil {
		return nil, nil, err
	}
	if len(srcSpans) == 0 {
		return nil, nil, fmt.Errorf("the source doesn't declare %s yet; adding it is not supported here", key)
	}
	var edits []textEdit
	used := map[int]bool{}
	for _, s := range srcSpans {
		j := -1
		for i, r := range rendered {
			if !used[i] && r.origin == s.origin {
				j = i
				break
			}
		}
		fc, err := pl.file(s.span.File)
		if err != nil {
			return nil, nil, err
		}
		if j < 0 {
			// The database's rendering has no such part: remove it.
			start, end := removalRange(scanutil.NormalizeSource(fc.Before), s.span, s.origin)
			edits = append(edits, textEdit{path: s.span.File, start: start, end: end, decision: d})
			continue
		}
		used[j] = true
		edits = append(edits, textEdit{path: s.span.File, start: s.span.Start, end: s.span.End,
			text: reindent(rendered[j].text, lineIndent(scanutil.NormalizeSource(fc.Before), s.span.Start)), decision: d})
	}
	for i, r := range rendered {
		if !used[i] {
			return nil, nil, fmt.Errorf("the database's %s has a %s part the source lacks; adding it is not supported here", key, r.origin)
		}
	}
	return edits, task, nil
}

// adoptGrant is planned in grants.go.

// originSpan is a span with the part of its declaration it lies in:
// "part1", "block" or "decl".
type originSpan struct {
	span   pipeline.Span
	origin string
	text   string
}

func spansWithOrigin(x *srcindex.Index, key, prop string) []originSpan {
	var out []originSpan
	for _, d := range x.Decls[key] {
		for _, sp := range d.Items[prop] {
			out = append(out, originSpan{span: sp, origin: originOf(d, sp)})
		}
	}
	return out
}

func originOf(d srcindex.Declaration, sp pipeline.Span) string {
	switch {
	case d.Part1.Valid() && sp.Start >= d.Part1.Start && sp.End <= d.Part1.End:
		return "part1"
	case d.Block.Valid() && sp.Start >= d.Block.Start && sp.End <= d.Block.End:
		return "block"
	}
	return "decl"
}

// renderedSpans renders the live object and returns the text of each part
// declaring key in that rendering.
func (pl *planner) renderedSpans(t *Target, it drift.Item, live pipeline.IRObject, key string, deps Deps) ([]originSpan, error) {
	text := deps.Render(live)
	dir, err := os.MkdirTemp("", "dpg-resolve-render-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	rel := relPath(t, t.NewFile(it.Kind, it.Object))
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return nil, err
	}
	idx, err := srcindex.Build([]string{path}, dir, pipeline.Default, t.CompileOpts)
	if err != nil {
		return nil, fmt.Errorf("rendering the database's %s: %v", it.Object, err)
	}
	spans := spansWithOrigin(idx, it.Object, key)
	for i := range spans {
		spans[i].text = text[spans[i].span.Start:spans[i].span.End]
	}
	return spans, nil
}

func relPath(t *Target, path string) string {
	if rel, err := filepath.Rel(t.SourceDir, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return filepath.Base(path)
}

// applyEdits applies text edits to the planned file contents, last first
// per file. Overlapping edits are a problem for the later decision.
func (pl *planner) applyEdits(edits []textEdit) error {
	byPath := map[string][]textEdit{}
	for _, e := range edits {
		byPath[e.path] = append(byPath[e.path], e)
	}
	for path, es := range byPath {
		sort.SliceStable(es, func(i, j int) bool { return es[i].start > es[j].start })
		fc, err := pl.file(path)
		if err != nil {
			return err
		}
		raw := fc.After
		crlf := bytes.Contains(raw, []byte("\r\n"))
		limit := len(scanutil.NormalizeSource(raw)) + 1
		for _, e := range es {
			if e.end > limit-1 && e.start != e.end {
				pl.problem(e.decision, "the edit is outside %s", path)
				continue
			}
			if e.end > limit {
				pl.problem(e.decision, "overlaps another adoption in %s", path)
				continue
			}
			s, en := scanutil.OriginalOffset(raw, e.start), scanutil.OriginalOffset(raw, e.end)
			text := e.text
			if crlf {
				text = strings.ReplaceAll(text, "\n", "\r\n")
			}
			raw = append(append(append([]byte(nil), raw[:s]...), text...), raw[en:]...)
			limit = e.start
		}
		fc.After = raw
	}
	return nil
}

// compileEdited compiles the target's source with the planned edits, in a
// scratch copy of its source directory.
func (pl *planner) compileEdited(t *Target, deps Deps) ([]pipeline.IRObject, error) {
	dir, err := os.MkdirTemp("", "dpg-resolve-compile-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	seen := map[string]bool{}
	var files []string
	write := func(path string, data []byte) error {
		rel := relPath(t, path)
		dst := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		files = append(files, dst)
		seen[path] = true
		return os.WriteFile(dst, data, 0o644)
	}
	for _, f := range t.SourceFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		if fc, ok := pl.files[f]; ok {
			data = fc.After
		}
		if err := write(f, data); err != nil {
			return nil, err
		}
	}
	for path, fc := range pl.files {
		if !seen[path] && strings.HasSuffix(path, ".dpg") && strings.HasPrefix(path, t.SourceDir) {
			if err := write(path, fc.After); err != nil {
				return nil, err
			}
		}
	}
	sort.Strings(files)
	return deps.Compile(files, dir, t.CompileOpts)
}

// applySnapTask updates the snapshot from the compiled, edited source.
func applySnapTask(snap *pipeline.Snapshot, src []pipeline.IRObject, task snapTask) error {
	if task.remove {
		delete(snap.Objects, task.key)
		return nil
	}
	obj := findObject(src, task.key)
	if obj == nil {
		return fmt.Errorf("%s: not in the adopted source", task.key)
	}
	tmp := &pipeline.Snapshot{}
	if err := snapshot.Populate(tmp, []pipeline.IRObject{obj}); err != nil {
		return err
	}
	fresh := tmp.Objects[task.key]
	if task.property == "" || snap.Objects[task.key] == nil {
		if snap.Objects == nil {
			snap.Objects = map[string]json.RawMessage{}
		}
		snap.Objects[task.key] = fresh
		return nil
	}
	patched, err := drift.PatchProperty(snap.Objects[task.key], fresh, task.property)
	if err != nil {
		return err
	}
	snap.Objects[task.key] = patched
	return nil
}

// wholeLines widens [start, end) to whole lines when only whitespace
// surrounds it on its lines, and takes one following blank line, so
// removing a declaration leaves no empty gap.
func wholeLines(text []byte, start, end int) (int, int) {
	s := start
	for s > 0 && (text[s-1] == ' ' || text[s-1] == '\t') {
		s--
	}
	e := end
	for e < len(text) && (text[e] == ' ' || text[e] == '\t') {
		e++
	}
	if (s == 0 || text[s-1] == '\n') && (e == len(text) || text[e] == '\n') {
		if e < len(text) {
			e++
		}
		if e < len(text) && text[e] == '\n' {
			e++
		}
		return s, e
	}
	return start, end
}

// removalRange is the range to delete to remove a part: a column-list
// entry with its separating comma, a block statement with its line.
func removalRange(text []byte, sp pipeline.Span, origin string) (int, int) {
	if origin == "part1" {
		// After the entry: ", " up to the next entry.
		i := sp.End
		for i < len(text) && (text[i] == ' ' || text[i] == '\t' || text[i] == '\n') {
			i++
		}
		if i < len(text) && text[i] == ',' {
			j := i + 1
			for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
				j++
			}
			if j < len(text) && text[j] == '\n' {
				// Next entry is on its own line: drop this entry's line.
				s, _ := wholeLines(text, sp.Start, sp.End)
				return s, j + 1
			}
			return sp.Start, j
		}
		// The last entry: drop the comma before it.
		k := sp.Start
		for k > 0 && (text[k-1] == ' ' || text[k-1] == '\t' || text[k-1] == '\n') {
			k--
		}
		if k > 0 && text[k-1] == ',' {
			return k - 1, sp.End
		}
		return sp.Start, sp.End
	}
	return wholeLines(text, sp.Start, sp.End)
}

// lineIndent returns the indentation of the line containing offset.
func lineIndent(text []byte, off int) string {
	s := off
	for s > 0 && text[s-1] != '\n' {
		s--
	}
	e := s
	for e < len(text) && (text[e] == ' ' || text[e] == '\t') {
		e++
	}
	return string(text[s:e])
}

// reindent re-indents the continuation lines of a snippet taken from the
// renderer to sit at indent.
func reindent(snippet, indent string) string {
	lines := strings.Split(snippet, "\n")
	if len(lines) == 1 {
		return snippet
	}
	// The renderer's indentation of the first continuation line is its base.
	base := ""
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) != "" {
			base = l[:len(l)-len(strings.TrimLeft(l, " \t"))]
			break
		}
	}
	for i := 1; i < len(lines); i++ {
		l := strings.TrimPrefix(lines[i], base)
		if strings.TrimSpace(l) == "" {
			lines[i] = ""
			continue
		}
		lines[i] = indent + "    " + l
		if i == len(lines)-1 && strings.HasPrefix(strings.TrimSpace(l), "}") {
			lines[i] = indent + strings.TrimSpace(l)
		}
	}
	return strings.Join(lines, "\n")
}
