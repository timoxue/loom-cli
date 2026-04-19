package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// PrintManifest renders a shadow Manifest as a human-readable table. The
// output is deliberately simple — it exists so operators can observe what an
// execution *would* promote without the real promotion ever happening.
// Actual workspace mutation is the responsibility of CommitGate.Promote,
// which is still gated behind an explicit user decision.
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

// CommitGate brokers the promotion boundary between a finished shadow
// workspace and the real workspace. It owns three operations:
//
//  1. LoadReceipt — resolve a session id to the admission Receipt that
//     was written by the compiler. O(1) lookup via the flattened cache
//     layout (<session>/receipt.json).
//  2. Preview — enumerate the changes that Promote would apply, without
//     touching the real workspace.
//  3. Promote — copy the shadow tree atomically into the real workspace.
//     This is the ONLY path that legitimately mutates workspace bytes;
//     `loom run` never invokes it.
//
// The gate itself holds no state — each call takes a Receipt so operators
// can load once, display, and then commit if approved.
type CommitGate struct{}

// LoadReceipt resolves a session id to its receipt. The session id is
// validated the same way it was at write time, so a path-escape attempt
// is rejected at the receipt lookup boundary rather than by the OS.
func (*CommitGate) LoadReceipt(sessionID string) (*Receipt, error) {
	receiptPath, err := ReceiptPathForSession(sessionID)
	if err != nil {
		return nil, err
	}

	raw, err := os.ReadFile(receiptPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &ContractError{
				Field:  "session_id",
				Reason: fmt.Sprintf("session %q not found (no receipt at %s)", sessionID, receiptPath),
			}
		}
		return nil, fmt.Errorf("read receipt %q: %w", receiptPath, err)
	}

	var receipt Receipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return nil, &ContractError{
			Field:  "receipt",
			Reason: fmt.Sprintf("decode receipt %q: %v", receiptPath, err),
		}
	}
	if receipt.ShadowPath == "" || receipt.WorkspaceRoot == "" {
		return nil, &ContractError{
			Field:  "receipt",
			Reason: "receipt is missing shadow_path or workspace_root — was it written by an older version?",
		}
	}
	return &receipt, nil
}

// Preview returns the manifest of pending changes for the receipt's
// session without touching the real workspace. The returned Changes are
// what Promote would apply, in the same order.
func (*CommitGate) Preview(receipt *Receipt) ([]Change, error) {
	if receipt == nil {
		return nil, &ContractError{
			Field:  "receipt",
			Reason: "receipt is nil",
		}
	}
	vfs := &ShadowVFS{
		WorkspaceDir: receipt.WorkspaceRoot,
		ShadowDir:    receipt.ShadowPath,
	}
	return vfs.Manifest()
}

// Promote atomically copies the shadow tree into the real workspace and
// removes the shadow directory on success. This is the only legitimate
// point of real-workspace mutation; `loom run` stops at the manifest.
//
// Failure semantics inherit from ShadowVFS.Commit: tombstones are applied
// first, then files are promoted via temp-file + rename. A mid-commit
// failure may leave partial workspace changes (a known limitation to be
// closed in Phase D).
func (*CommitGate) Promote(receipt *Receipt) error {
	if receipt == nil {
		return &ContractError{
			Field:  "receipt",
			Reason: "receipt is nil",
		}
	}
	vfs := &ShadowVFS{
		WorkspaceDir: receipt.WorkspaceRoot,
		ShadowDir:    receipt.ShadowPath,
	}
	return vfs.Commit()
}
