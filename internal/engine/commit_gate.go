package engine

import (
	"fmt"
	"io"
)

// PrintManifest renders a shadow Manifest as a human-readable table. The
// output is deliberately simple — it exists so operators can observe what an
// execution *would* promote without the real promotion ever happening. Real
// workspace mutation is the responsibility of ShadowVFS.Commit, which is
// still gated behind an explicit user decision.
func PrintManifest(w io.Writer, changes []Change) {
	if len(changes) == 0 {
		fmt.Fprintln(w, "Manifest: (no shadow changes)")
		return
	}

	fmt.Fprintln(w, "Manifest:")
	fmt.Fprintln(w, "  OP      PATH")
	for _, change := range changes {
		fmt.Fprintf(w, "  %-7s %s\n", change.Op, change.Path)
	}
}
