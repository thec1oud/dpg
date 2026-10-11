// Package workspace manages the open-document cache and drives recompilation
// by shelling out to the dpg CLI.
package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Diagnostic mirrors the JSON output of `dpg validate --format json`.
type Diagnostic struct {
	Rule    string `json:"rule"`
	Message string `json:"message"`
	File    string `json:"file,omitempty"`
	Line    int    `json:"line,omitempty"`
	Col     int    `json:"col,omitempty"`
	IsError bool   `json:"-"`
}

// Document holds the current text of an open file and a debounce timer.
type Document struct {
	Text  string
	timer *time.Timer
	mu    sync.Mutex
}

// Workspace tracks open documents, the project root, and the latest diagnostics.
type Workspace struct {
	mu          sync.RWMutex
	root        string
	docs        map[string]*Document
	diagnostics map[string][]Diagnostic
	onChange    func(path string)
}

func New() *Workspace {
	return &Workspace{
		docs:        make(map[string]*Document),
		diagnostics: make(map[string][]Diagnostic),
	}
}

// SetRoot sets the project root directory.
func (w *Workspace) SetRoot(root string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.root = root
}

// Root returns the project root.
func (w *Workspace) Root() string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.root
}

// SetOnChange registers a callback invoked after diagnostics update for a file.
func (w *Workspace) SetOnChange(fn func(path string)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onChange = fn
}

// Discover walks up from root to find dpg.toml (no-op for now; root is set at Initialize).
func (w *Workspace) Discover() {}

// OpenDocument adds a document to the cache.
func (w *Workspace) OpenDocument(path, text string) {
	w.mu.Lock()
	doc := &Document{Text: text}
	w.docs[path] = doc
	w.mu.Unlock()
	w.scheduleValidate(path)
}

// UpdateDocument replaces the cached text and reschedules validation.
func (w *Workspace) UpdateDocument(path, text string) {
	w.mu.Lock()
	doc, ok := w.docs[path]
	if !ok {
		doc = &Document{}
		w.docs[path] = doc
	}
	doc.mu.Lock()
	doc.Text = text
	doc.mu.Unlock()
	w.mu.Unlock()
	w.scheduleValidate(path)
}

// CloseDocument removes a document from the cache.
func (w *Workspace) CloseDocument(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if doc, ok := w.docs[path]; ok {
		doc.mu.Lock()
		if doc.timer != nil {
			doc.timer.Stop()
		}
		doc.mu.Unlock()
	}
	delete(w.docs, path)
	delete(w.diagnostics, path)
}

// GetText returns the in-memory text for a file, or reads from disk if not open.
func (w *Workspace) GetText(path string) string {
	w.mu.RLock()
	doc, ok := w.docs[path]
	w.mu.RUnlock()
	if ok {
		doc.mu.Lock()
		t := doc.Text
		doc.mu.Unlock()
		return t
	}
	data, _ := os.ReadFile(path)
	return string(data)
}

// GetDiagnostics returns cached diagnostics for a file.
func (w *Workspace) GetDiagnostics(path string) []Diagnostic {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.diagnostics[path]
}

// SetDiagnostics stores diagnostics for a file.
func (w *Workspace) SetDiagnostics(path string, diags []Diagnostic) {
	w.mu.Lock()
	w.diagnostics[path] = diags
	cb := w.onChange
	w.mu.Unlock()
	if cb != nil {
		cb(path)
	}
}

// scheduleValidate debounces validation: waits 300ms after the last change.
func (w *Workspace) scheduleValidate(path string) {
	w.mu.Lock()
	doc, ok := w.docs[path]
	if !ok {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()

	doc.mu.Lock()
	if doc.timer != nil {
		doc.timer.Stop()
	}
	doc.timer = time.AfterFunc(300*time.Millisecond, func() {
		w.validate(path)
	})
	doc.mu.Unlock()
}

// validate writes the current document text to a temp file (if unsaved),
// runs `dpg validate --format json`, and stores the results.
func (w *Workspace) validate(path string) {
	root := w.Root()
	if root == "" {
		// Try to find root from file path
		root = FindProjectRoot(path)
	}

	// Write in-memory content to a temp file so dpg can read it
	text := w.GetText(path)
	tmp, err := writeTempFile(path, text)
	if err != nil {
		return
	}
	defer os.Remove(tmp)

	diags := runValidate(root, tmp, path)
	w.SetDiagnostics(path, diags)
}

// writeTempFile writes content to a temp file with the same extension as orig.
func writeTempFile(orig, content string) (string, error) {
	ext := filepath.Ext(orig)
	f, err := os.CreateTemp("", "dpg-lsp-*"+ext)
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(content)
	f.Close()
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// ObjectInfo is a parsed representation of a top-level object for hover/definition.
type ObjectInfo struct {
	Kind    string // TABLE, VIEW, FUNCTION, etc.
	Name    string // schema.name or name
	Comment string
	File    string
	Line    int
}

// ParseObjects performs a very lightweight scan of a .dpg file to extract
// object names and positions (for hover and go-to-definition).
// This avoids needing the full DPG compiler as a library.
func ParseObjects(text, filePath string) []ObjectInfo {
	var objs []ObjectInfo
	lines := strings.Split(text, "\n")

	// Compound keywords checked before single-word keywords.
	type kwEntry struct {
		prefix string // full prefix to match (uppercased), e.g. "VIRTUAL TYPE "
		kind   string // reported Kind, e.g. "VIRTUAL TYPE"
		nameAt int    // byte offset past the prefix to start extracting the name
	}
	var kwEntries []kwEntry
	for _, compound := range []string{"UNLOGGED TABLE", "FOREIGN TABLE", "MATERIALIZED VIEW", "RECURSIVE VIEW", "VIRTUAL TYPE"} {
		kwEntries = append(kwEntries, kwEntry{compound + " ", compound, len(compound) + 1})
	}
	for _, simple := range []string{"TABLE", "VIEW", "FUNCTION", "PROCEDURE", "AGGREGATE",
		"TYPE", "DOMAIN", "SCHEMA", "SEQUENCE", "ROLE",
		"EXTENSION", "PUBLICATION", "SUBSCRIPTION", "MACRO"} {
		kwEntries = append(kwEntries, kwEntry{simple + " ", simple, len(simple) + 1})
	}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		upper := strings.ToUpper(trimmed)
		for _, e := range kwEntries {
			if strings.HasPrefix(upper, e.prefix) {
				rest := strings.TrimSpace(trimmed[e.nameAt:])
				fields := strings.Fields(rest)
				if len(fields) > 0 {
					name := fields[0]
					if idx := strings.Index(name, "("); idx >= 0 {
						name = name[:idx]
					}
					kind := e.kind
					// TYPE name AS ENUM (...) is reported as an ENUM so
					// outlines and completions can tell it apart.
					if kind == "TYPE" && len(fields) > 2 &&
						strings.EqualFold(fields[1], "AS") && strings.HasPrefix(strings.ToUpper(fields[2]), "ENUM") {
						kind = "ENUM"
					}
					objs = append(objs, ObjectInfo{
						Kind: kind,
						Name: name,
						File: filePath,
						Line: i + 1,
					})
				}
				break
			}
		}
	}
	return objs
}
