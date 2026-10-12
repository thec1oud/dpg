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
	"github.com/thec1oud/dpg/internal/format"
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
	// once marks an insertion several decisions may each make (the comma
	// after a list's last entry): it is applied once.
	once bool
	// listEntry marks a new list entry placed on its own line after a
	// comment: when several go to the same place, each but the last gets
	// the separating comma.
	listEntry bool
	seq       int // the adoption it belongs to
}

// snapTask updates one snapshot entry from the edited source: the whole
// entry (property == ""), one property, or removes it (remove).
type snapTask struct {
	key, property string
	remove        bool
}

// adoptAll plans adoptions: text edits to the source, then the snapshot
// entries recomputed from the edited source (compiled in a scratch copy, so
// a broken edit is caught before anything is written). When the edits
// together don't compile, each decision is compiled on its own and only
// the ones that break the source are refused.
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
	type adoption struct {
		d     Decision
		edits []textEdit
		tasks []snapTask
	}
	all := func(as []adoption) []textEdit {
		var out []textEdit
		for _, a := range as {
			out = append(out, a.edits...)
		}
		return out
	}
	for _, t := range order {
		if t.LogErr != nil {
			for _, d := range byTarget[t] {
				pl.problem(d, "adopting changes the snapshot, which needs a migration log: %v", t.LogErr)
			}
			continue
		}
		var as []adoption
		for _, d := range byTarget[t] {
			it, _ := findItem(t.Report, d.ItemID)
			e, task, err := pl.adoptOne(t, it, d, deps)
			if err != nil {
				pl.problem(d, "%v", err)
				continue
			}
			for i := range e {
				e[i].seq = len(as)
			}
			as = append(as, adoption{d, e, task})
		}
		// Edits that can't be applied refuse their decision.
		if _, rejected, err := pl.edited(all(as)); err != nil {
			return err
		} else if len(rejected) > 0 {
			var keep []adoption
			for i, a := range as {
				if why, bad := rejected[i]; bad {
					pl.problem(a.d, "%s", why)
				} else {
					keep = append(keep, a)
				}
			}
			as = keep
		}
		if len(as) == 0 {
			continue
		}
		src, err := pl.compileEdited(t, all(as), deps)
		if err != nil && len(as) > 1 {
			var keep []adoption
			for _, a := range as {
				if _, err := pl.compileEdited(t, a.edits, deps); err != nil {
					pl.problem(a.d, "the adopted source doesn't compile: %v", err)
				} else {
					keep = append(keep, a)
				}
			}
			as = keep
			if len(as) == 0 {
				continue
			}
			src, err = pl.compileEdited(t, all(as), deps)
		}
		if err != nil {
			for _, a := range as {
				pl.problem(a.d, "the adopted source doesn't compile: %v", err)
			}
			continue
		}
		files, _, err := pl.edited(all(as))
		if err != nil {
			return err
		}
		for path, data := range files {
			fc, err := pl.file(path)
			if err != nil {
				return err
			}
			fc.After = data
		}
		var tasks []snapTask
		for _, a := range as {
			tasks = append(tasks, a.tasks...)
		}
		if len(tasks) == 0 {
			continue
		}
		snap := pl.snapshot(t)
		for _, task := range tasks {
			if err := applySnapTask(snap, src, task); err != nil {
				return err
			}
		}
		if err := pl.migrationSQL(t, src, deps); err != nil {
			return err
		}
	}
	return nil
}

// migrationSQL sets the migration of the target's snapshot change: the differ's
// ops from the stored snapshot to the adopted source, for the objects the
// change touches only (the source's other pending edits aren't adopted).
func (pl *planner) migrationSQL(t *Target, src []pipeline.IRObject, deps Deps) error {
	sc := pl.snaps[t]
	if sc == nil || deps.Emit == nil {
		return nil
	}
	changed := map[string]bool{}
	for k, v := range sc.After.Objects {
		if old, ok := sc.Before.Objects[k]; !ok || !bytes.Equal(old, v) {
			changed[k] = true
		}
	}
	for k := range sc.Before.Objects {
		if _, ok := sc.After.Objects[k]; !ok {
			changed[k] = true
		}
	}
	ops, err := deps.Differ.Diff(src, t.Snapshot)
	if err != nil {
		return err
	}
	var keep []pipeline.DiffOp
	for _, op := range ops {
		if to, ok := op.(pipeline.TargetedOp); ok && (changed[to.Target().Key] || changed[to.Target().From]) {
			keep = append(keep, op)
		}
	}
	m, err := deps.Emit(keep)
	if err != nil {
		return err
	}
	sc.Migration = m
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
	srcSpans := spansWithOrigin(t.Index, it.Object, key, func(file string) []byte {
		fc, err := pl.file(file)
		if err != nil {
			return nil
		}
		return scanutil.NormalizeSource(fc.Before)
	})
	task := []snapTask{{key: it.Object, property: it.Property}}

	if it.Change == "missing" {
		if len(srcSpans) == 0 {
			return nil, nil, fmt.Errorf("no source text declares %s", key)
		}
		var edits []textEdit
		for _, s := range srcSpans {
			e, err := pl.partEdit(t, s, nil, d)
			if err != nil {
				return nil, nil, err
			}
			edits = append(edits, e...)
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
		if j < 0 {
			// The database's rendering has no such part: remove it.
			e, err := pl.partEdit(t, s, nil, d)
			if err != nil {
				return nil, nil, err
			}
			edits = append(edits, e...)
			continue
		}
		used[j] = true
		// The rendering puts the part in groups the source may not use
		// (INDEXES { ... } for INDEX ...;): the replacement brings them.
		r := rendered[j]
		if len(s.groups) > len(r.groups) || !sameGroups(s.groups, r.groups[:len(s.groups)]) {
			return nil, nil, fmt.Errorf("%s is declared inside %q in the source but %q in the database's rendering",
				key, strings.Join(s.groups, " / "), strings.Join(r.groups, " / "))
		}
		text := r.text
		if extra := r.groups[len(s.groups):]; len(extra) > 0 {
			if s.macro != nil {
				return nil, nil, fmt.Errorf("%s comes from macro %s and would need its groups changed; edit it by hand", key, s.span.Macro)
			}
			text = wrapGroups(extra, r.text, "") // partEdit indents it
		}
		e, err := pl.partEdit(t, s, &text, d)
		if err != nil {
			return nil, nil, err
		}
		edits = append(edits, e...)
	}
	for i, r := range rendered {
		if !used[i] {
			e, err := pl.insertPart(t, it, d, r)
			if err != nil {
				return nil, nil, err
			}
			edits = append(edits, e...)
		}
	}
	if len(edits) == 0 {
		return nil, nil, fmt.Errorf("neither the source nor the database's rendering declares %s", key)
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
	// groups are the headers of the block groups the part is inside, and
	// blockStart the start of its declaration's block (block parts only).
	groups     []string
	blockStart int
	// macro locates a part a macro spread produced (span is the spread).
	macro *srcindex.MacroPart
}

// spansWithOrigin returns the spans declaring prop with their origins;
// text, when given, is each span's file content, for the groups of block
// parts.
func spansWithOrigin(x *srcindex.Index, key, prop string, text func(file string) []byte) []originSpan {
	var out []originSpan
	for _, d := range x.Decls[key] {
		for _, sp := range d.Items[prop] {
			o := originSpan{span: sp, origin: originOf(d, sp), blockStart: d.Block.Start}
			if mp, ok := d.MacroParts[sp]; ok {
				o.macro = &mp
			}
			if o.origin == "block" && text != nil {
				if t := text(sp.File); t != nil {
					o.groups = enclosingGroups(t, d.Block.Start, sp.Start)
				}
			}
			out = append(out, o)
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
	spans := spansWithOrigin(idx, it.Object, key, func(string) []byte { return []byte(text) })
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

// edited returns the planned contents of every file the edits touch, with
// the edits applied, last first per file; edits at the same offset keep
// their adoptions' order. An edit outside its file or overlapping another
// refuses its adoption: rejected maps the adoption's seq to why.
func (pl *planner) edited(edits []textEdit) (map[string][]byte, map[int]string, error) {
	byPath := map[string][]textEdit{}
	for _, e := range edits {
		byPath[e.path] = append(byPath[e.path], e)
	}
	out := map[string][]byte{}
	rejected := map[int]string{}
	for path, es := range byPath {
		sort.SliceStable(es, func(i, j int) bool {
			if es[i].start != es[j].start {
				return es[i].start > es[j].start
			}
			return es[i].seq > es[j].seq
		})
		fc, err := pl.file(path)
		if err != nil {
			return nil, nil, err
		}
		raw := fc.After
		crlf := bytes.Contains(raw, []byte("\r\n"))
		size := len(scanutil.NormalizeSource(raw))
		limit := size // the start of the last edit applied
		applied := map[string]bool{}
		entryAt := map[int]bool{}
		for _, e := range es {
			if e.end > size || e.start > e.end {
				rejected[e.seq] = "the edit is outside " + path
				continue
			}
			if e.end > limit {
				rejected[e.seq] = "overlaps another adoption in " + path
				continue
			}
			if e.once {
				k := fmt.Sprintf("%d/%s", e.start, e.text)
				if applied[k] {
					continue
				}
				applied[k] = true
			}
			text := e.text
			if e.listEntry {
				if entryAt[e.start] {
					text += ","
				}
				entryAt[e.start] = true
			}
			s, en := scanutil.OriginalOffset(raw, e.start), scanutil.OriginalOffset(raw, e.end)
			if crlf {
				text = strings.ReplaceAll(text, "\n", "\r\n")
			}
			raw = append(append(append([]byte(nil), raw[:s]...), text...), raw[en:]...)
			limit = e.start
		}
		out[path] = raw
	}
	return out, rejected, nil
}

// compileEdited compiles the target's source with the planned edits, in a
// scratch copy of its source directory.
func (pl *planner) compileEdited(t *Target, edits []textEdit, deps Deps) ([]pipeline.IRObject, error) {
	changed, _, err := pl.edited(edits)
	if err != nil {
		return nil, err
	}
	content := func(path string) ([]byte, bool) {
		if data, ok := changed[path]; ok {
			return data, true
		}
		if fc, ok := pl.files[path]; ok {
			return fc.After, true
		}
		return nil, false
	}
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
		data, ok := content(f)
		if !ok {
			if data, err = os.ReadFile(f); err != nil {
				return nil, err
			}
		}
		if err := write(f, data); err != nil {
			return nil, err
		}
	}
	// New files (adopted objects).
	var extra []string
	for path := range changed {
		extra = append(extra, path)
	}
	for path := range pl.files {
		extra = append(extra, path)
	}
	for _, path := range extra {
		if !seen[path] && strings.HasSuffix(path, ".dpg") && strings.HasPrefix(path, t.SourceDir) {
			data, _ := content(path)
			if len(data) == 0 {
				continue
			}
			if err := write(path, data); err != nil {
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
// surrounds it on its lines: the newline before it goes too (the one after
// on the first line), and a blank line before it when one follows, so
// removing a declaration leaves no gap. Ending at the removed text, not
// after its newline, lets another adoption insert right after it.
func wholeLines(text []byte, start, end int) (int, int) {
	s := start
	for s > 0 && (text[s-1] == ' ' || text[s-1] == '\t') {
		s--
	}
	e := end
	for e < len(text) && (text[e] == ' ' || text[e] == '\t') {
		e++
	}
	if !((s == 0 || text[s-1] == '\n') && (e == len(text) || text[e] == '\n')) {
		return start, end
	}
	if s == 0 {
		if e < len(text) {
			e++
		}
		if e < len(text) && text[e] == '\n' {
			e++
		}
		return s, e
	}
	s--
	if s > 0 && text[s-1] == '\n' && (e+1 >= len(text) || text[e+1] == '\n') {
		s--
	}
	return s, e
}

// removalRange is the range to delete to remove a part: a column-list
// entry with its separating comma, a block statement with its line, and a
// group the statement was the last entry of with it.
func removalRange(text []byte, o originSpan) (int, int) {
	sp := o.span
	if o.origin == "part1" {
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
				// Next entry is on its own line: drop this entry's line
				// when the entry starts it.
				s := sp.Start
				for s > 0 && (text[s-1] == ' ' || text[s-1] == '\t') {
					s--
				}
				if s == 0 || text[s-1] == '\n' {
					return s, j + 1
				}
				return sp.Start, j + 1
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
	start, end := sp.Start, sp.End
	if o.origin == "block" {
		start, end = emptiedGroups(text, o.blockStart, start, end)
	}
	return wholeLines(text, start, end)
}

// emptiedGroups widens [start, end), a statement being removed, to the
// group statement around it when it is the group's only entry, and so on
// outwards: removing the last index removes INDEXES { }.
func emptiedGroups(text []byte, blockStart, start, end int) (int, int) {
	for {
		gs, ge, ok := groupAround(text, blockStart, start, end)
		if !ok {
			return start, end
		}
		start, end = gs, ge
	}
}

// groupAround finds the group statement directly enclosing [start, end)
// within the block starting at blockStart. ok is false when there is none
// or it holds anything besides [start, end).
func groupAround(text []byte, blockStart, start, end int) (int, int, bool) {
	type open struct{ header, brace int }
	var stack []open
	header := -1
	for _, t := range lexAt(string(text[blockStart:start]), blockStart) {
		switch t.typ {
		case format.TokLBrace:
			stack = append(stack, open{header, t.end})
			header = -1
		case format.TokRBrace:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			header = -1
		case format.TokSemicolon:
			header = -1
		default:
			if header < 0 && !isTriviaTok(t) {
				header = t.start
			}
		}
	}
	if len(stack) == 0 || stack[len(stack)-1].header < 0 {
		return 0, 0, false
	}
	g := stack[len(stack)-1]
	// Nothing but trivia between "{" and start ...
	for _, t := range lexAt(string(text[g.brace:start]), g.brace) {
		if !isTriviaTok(t) {
			return 0, 0, false
		}
	}
	// ... and between end and the group's "}".
	depth := 0
	for _, t := range lexAt(string(text[end:]), end) {
		switch {
		case t.typ == format.TokRBrace && depth == 0:
			e := t.end
			// An optional ";" after the group.
			for _, u := range lexAt(string(text[e:]), e) {
				if u.typ == format.TokSemicolon {
					e = u.end
				}
				if !isTriviaTok(u) {
					break
				}
			}
			return g.header, e, true
		case t.typ == format.TokLBrace:
			depth++
		case t.typ == format.TokRBrace:
			depth--
		case !isTriviaTok(t):
			return 0, 0, false
		}
	}
	return 0, 0, false
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

func sameGroups(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if headerName(a[i]) != headerName(b[i]) {
			return false
		}
	}
	return true
}
