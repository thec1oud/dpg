// Package emit implements pipeline.Emitter. It splits DiffOps into transactional
// and non-transactional groups and wraps them in a Migration.
package emit

import (
	"github.com/thec1oud/dpg/internal/pipeline"
)

func init() {
	pipeline.Default.Register(pipeline.KeyEmitter, New())
}

// Emitter implements pipeline.Emitter.
type Emitter struct{}

// New returns an Emitter.
func New() *Emitter { return &Emitter{} }

// Emit splits ops into transactional and non-transactional slices by Mode.
// Transactional ops are intended to be wrapped in BEGIN/COMMIT by the executor.
// Non-transactional ops (e.g. CREATE INDEX CONCURRENTLY, DROP INDEX
// CONCURRENTLY) must run outside any transaction block. Instruction-only ops
// are kept in order with the non-transactional section for display; callers
// never execute them.
func (e *Emitter) Emit(ops []pipeline.DiffOp, meta pipeline.MigrationMeta) (pipeline.Migration, error) {
	m := pipeline.Migration{Meta: meta}
	for _, o := range ops {
		switch o.Mode() {
		case pipeline.ModeTransactional:
			m.Transactional = append(m.Transactional, o)
		case pipeline.ModePreTransactional:
			m.PreTransactional = append(m.PreTransactional, o)
		default:
			m.NonTransactional = append(m.NonTransactional, o)
		}
	}
	return m, nil
}

var _ pipeline.Emitter = (*Emitter)(nil)
