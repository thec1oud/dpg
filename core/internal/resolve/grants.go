package resolve

import (
	"fmt"

	"github.com/thec1oud/dpg/internal/drift"
)

// adoptGrant plans adopting a grant item (an extra live grant, a declared
// grant missing live, a revoked privilege granted again).
func (pl *planner) adoptGrant(t *Target, it drift.Item, d Decision) ([]textEdit, []snapTask, error) {
	return nil, nil, fmt.Errorf("adopting grants is not implemented yet")
}
