package engine

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	shadowMetadataDirName   = ".loom_meta"
	shadowTombstonesDirName = "tombstones"
	tombstoneFileSuffix     = ".tombstone"
)

// ShadowVFS isolates all agent writes into a disposable shadow tree until commit time.
type ShadowVFS struct {
	WorkspaceDir string // Real project root used as the immutable read baseline before commit.
	ShadowDir    string // Isolated write target that absorbs all agent-side mutations for the current session.
}

// ResolveReadPath maps a requested path to the newest readable file, preferring shadow state over workspace state.
func (v *ShadowVFS) ResolveReadPath(requestedPath string) (string, error) {
	workspaceRoot, shadowRoot, err := v.normalizedRoots()
	if err != nil {
		return "", err
	}

	relativePath, err := resolveManagedRelativePath(requestedPath, workspaceRoot, shadowRoot)
	if err != nil {
		return "", err
	}

	shadowPath, err := joinWithinBase(shadowRoot, relativePath)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(shadowPath); err == nil {
		return shadowPath, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat shadow path %q: %w", shadowPath, err)
	}

	if deleted, err := hasTombstoneForRelativePath(shadowRoot, relativePath); err != nil {
		return "", err
	} else if deleted {
		return "", &os.PathError{
			Op:   "open",
			Path: requestedPath,
			Err:  os.ErrNotExist,
		}
	}

	workspacePath, err := joinWithinBase(workspaceRoot, relativePath)
	if err != nil {
		return "", err
	}

	return workspacePath, nil
}

// ResolveWritePath forces every write target into the shadow tree while preserving its workspace-relative location.
func (v *ShadowVFS) ResolveWritePath(requestedPath string) (string, error) {
	workspaceRoot, shadowRoot, err := v.normalizedRoots()
	if err != nil {
		return "", err
	}

	relativePath, err := resolveManagedRelativePath(requestedPath, workspaceRoot, shadowRoot)
	if err != nil {
		return "", err
	}

	return joinWithinBase(shadowRoot, relativePath)
}

// MarkDeleted records a deletion intent in the shadow workspace without mutating the real workspace.
func (v *ShadowVFS) MarkDeleted(requestedPath string) error {
	workspaceRoot, shadowRoot, err := v.normalizedRoots()
	if err != nil {
		return err
	}

	relativePath, err := resolveManagedRelativePath(requestedPath, workspaceRoot, shadowRoot)
	if err != nil {
		return err
	}
	if relativePath == "." {
		return &SecurityError{
			Field:  requestedPath,
			Reason: "refusing to delete the workspace root",
		}
	}

	shadowPath, err := joinWithinBase(shadowRoot, relativePath)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(shadowPath); err != nil {
		return fmt.Errorf("remove shadow content %q: %w", shadowPath, err)
	}

	tombstonePath, err := tombstoneMarkerPath(shadowRoot, relativePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(tombstonePath), 0o755); err != nil {
		return fmt.Errorf("create tombstone parent directory %q: %w", filepath.Dir(tombstonePath), err)
	}
	if err := os.WriteFile(tombstonePath, []byte(relativePath+"\n"), 0o644); err != nil {
		return fmt.Errorf("write tombstone %q: %w", tombstonePath, err)
	}

	return nil
}

// ChangeOp names the kind of mutation recorded in a shadow manifest entry.
type ChangeOp string

const (
	ChangeOpWrite  ChangeOp = "write"
	ChangeOpDelete ChangeOp = "delete"
)

// Change records a single filesystem mutation inside the shadow tree. The
// Path is workspace-relative and forward-slash normalized for display and
// hash stability.
type Change struct {
	Op   ChangeOp `json:"op"`
	Path string   `json:"path"`
}

// Manifest walks the shadow tree and returns the full set of pending
// mutations. Write entries are file paths that will be promoted on commit;
// delete entries correspond to tombstones. The shadow metadata namespace
// itself is excluded.
func (v *ShadowVFS) Manifest() ([]Change, error) {
	_, shadowRoot, err := v.normalizedRoots()
	if err != nil {
		return nil, err
	}

	changes := make([]Change, 0, 8)

	shadowInfo, err := os.Stat(shadowRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return changes, nil
		}
		return nil, fmt.Errorf("stat shadow dir %q: %w", shadowRoot, err)
	}
	if !shadowInfo.IsDir() {
		return nil, &ContractError{
			Field:  "shadow_dir",
			Reason: fmt.Sprintf("shadow path %q is not a directory", shadowRoot),
		}
	}

	tombstoneRoot := tombstoneRootPath(shadowRoot)

	if err := filepath.WalkDir(shadowRoot, func(currentPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk shadow path %q: %w", currentPath, walkErr)
		}
		if currentPath == shadowRoot {
			return nil
		}
		if currentPath == filepath.Join(shadowRoot, shadowMetadataDirName) {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}

		relative, err := filepath.Rel(shadowRoot, currentPath)
		if err != nil {
			return fmt.Errorf("derive shadow relative path for %q: %w", currentPath, err)
		}
		changes = append(changes, Change{
			Op:   ChangeOpWrite,
			Path: filepath.ToSlash(filepath.Clean(relative)),
		})
		return nil
	}); err != nil {
		return nil, err
	}

	if _, err := os.Stat(tombstoneRoot); err == nil {
		if err := filepath.WalkDir(tombstoneRoot, func(currentPath string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return fmt.Errorf("walk tombstone path %q: %w", currentPath, walkErr)
			}
			if entry.IsDir() {
				return nil
			}
			relativeMarker, err := filepath.Rel(tombstoneRoot, currentPath)
			if err != nil {
				return fmt.Errorf("derive tombstone relative path for %q: %w", currentPath, err)
			}
			if !strings.HasSuffix(relativeMarker, tombstoneFileSuffix) {
				return nil
			}
			relative := strings.TrimSuffix(relativeMarker, tombstoneFileSuffix)
			changes = append(changes, Change{
				Op:   ChangeOpDelete,
				Path: filepath.ToSlash(filepath.Clean(relative)),
			})
			return nil
		}); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat tombstone root %q: %w", tombstoneRoot, err)
	}

	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Op != changes[j].Op {
			return changes[i].Op < changes[j].Op
		}
		return changes[i].Path < changes[j].Path
	})
	return changes, nil
}

// Commit promotes shadow changes into the real workspace and removes the shadow tree on success.
func (v *ShadowVFS) Commit() error {
	workspaceRoot, shadowRoot, err := v.normalizedRoots()
	if err != nil {
		return err
	}

	shadowInfo, err := os.Stat(shadowRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat shadow dir %q: %w", shadowRoot, err)
	}
	if !shadowInfo.IsDir() {
		return &ContractError{
			Field:  "shadow_dir",
			Reason: fmt.Sprintf("shadow path %q is not a directory", shadowRoot),
		}
	}

	if err := applyTombstones(workspaceRoot, shadowRoot); err != nil {
		return err
	}

	if err := filepath.WalkDir(shadowRoot, func(currentPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk shadow path %q: %w", currentPath, walkErr)
		}

		relativePath, err := filepath.Rel(shadowRoot, currentPath)
		if err != nil {
			return fmt.Errorf("derive shadow relative path for %q: %w", currentPath, err)
		}
		if relativePath == "." {
			return nil
		}

		cleanedRelativePath := filepath.Clean(relativePath)
		if cleanedRelativePath == shadowMetadataDirName {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return &SecurityError{
				Field:  currentPath,
				Reason: "shadow metadata path must be a directory",
			}
		}
		if escapesBase(cleanedRelativePath) {
			return &SecurityError{
				Field:  currentPath,
				Reason: "shadow entry escapes shadow root",
			}
		}

		targetPath, err := joinWithinBase(workspaceRoot, cleanedRelativePath)
		if err != nil {
			return err
		}

		if entry.Type()&os.ModeSymlink != 0 {
			return &SecurityError{
				Field:  currentPath,
				Reason: "shadow commit rejects symbolic links",
			}
		}
		if entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return fmt.Errorf("stat shadow directory %q: %w", currentPath, err)
			}
			return ensureTargetDirectory(targetPath, info.Mode().Perm())
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat shadow entry %q: %w", currentPath, err)
		}
		if !info.Mode().IsRegular() {
			return &SecurityError{
				Field:  currentPath,
				Reason: fmt.Sprintf("shadow commit rejects non-regular file mode %v", info.Mode()),
			}
		}

		return copyFileIntoWorkspace(currentPath, targetPath, info.Mode().Perm())
	}); err != nil {
		return err
	}

	if err := os.RemoveAll(shadowRoot); err != nil {
		return fmt.Errorf("remove shadow dir %q: %w", shadowRoot, err)
	}

	return nil
}

func (v *ShadowVFS) normalizedRoots() (string, string, error) {
	workspaceRoot, err := normalizeRootPath(v.WorkspaceDir)
	if err != nil {
		return "", "", &ContractError{
			Field:  "workspace_dir",
			Reason: err.Error(),
		}
	}

	shadowRoot, err := normalizeRootPath(v.ShadowDir)
	if err != nil {
		return "", "", &ContractError{
			Field:  "shadow_dir",
			Reason: err.Error(),
		}
	}

	if samePath(workspaceRoot, shadowRoot) {
		return "", "", &SecurityError{
			Field:  "shadow_dir",
			Reason: "shadow directory must not be identical to workspace directory",
		}
	}
	if within, err := isWithinBase(workspaceRoot, shadowRoot); err != nil {
		return "", "", err
	} else if within {
		return "", "", &SecurityError{
			Field:  "shadow_dir",
			Reason: "shadow directory must not be nested inside workspace directory",
		}
	}
	if within, err := isWithinBase(shadowRoot, workspaceRoot); err != nil {
		return "", "", err
	} else if within {
		return "", "", &SecurityError{
			Field:  "workspace_dir",
			Reason: "workspace directory must not be nested inside shadow directory",
		}
	}

	return workspaceRoot, shadowRoot, nil
}

func normalizeRootPath(rawPath string) (string, error) {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return "", fmt.Errorf("path must not be empty")
	}

	absolutePath, err := filepath.Abs(filepath.Clean(filepath.FromSlash(trimmed)))
	if err != nil {
		return "", fmt.Errorf("normalize path %q: %w", rawPath, err)
	}

	return absolutePath, nil
}

func resolveManagedRelativePath(requestedPath, workspaceRoot, shadowRoot string) (string, error) {
	trimmed := strings.TrimSpace(requestedPath)
	if trimmed == "" {
		return "", &ContractError{
			Field:  "path",
			Reason: "requested path must not be empty",
		}
	}

	normalized := filepath.Clean(filepath.FromSlash(trimmed))
	if filepath.VolumeName(normalized) != "" && !filepath.IsAbs(normalized) {
		return "", &SecurityError{
			Field:  requestedPath,
			Reason: "drive-qualified relative paths are not allowed",
		}
	}

	if filepath.IsAbs(normalized) {
		if relativePath, ok, err := relativeIfWithinBase(workspaceRoot, normalized); err != nil {
			return "", err
		} else if ok {
			if err := validateManagedRelativePath(requestedPath, relativePath); err != nil {
				return "", err
			}
			return relativePath, nil
		}

		if relativePath, ok, err := relativeIfWithinBase(shadowRoot, normalized); err != nil {
			return "", err
		} else if ok {
			if err := validateManagedRelativePath(requestedPath, relativePath); err != nil {
				return "", err
			}
			return relativePath, nil
		}

		return "", &SecurityError{
			Field:  requestedPath,
			Reason: "requested path escapes managed roots",
		}
	}

	candidatePath, err := joinWithinBase(workspaceRoot, normalized)
	if err != nil {
		return "", &SecurityError{
			Field:  requestedPath,
			Reason: "requested path escapes workspace root",
		}
	}

	relativePath, ok, err := relativeIfWithinBase(workspaceRoot, candidatePath)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", &SecurityError{
			Field:  requestedPath,
			Reason: "requested path escapes workspace root",
		}
	}
	if err := validateManagedRelativePath(requestedPath, relativePath); err != nil {
		return "", err
	}

	return relativePath, nil
}

func validateManagedRelativePath(requestedPath, relativePath string) error {
	if !isReservedShadowRelativePath(relativePath) {
		return nil
	}

	return &SecurityError{
		Field:  requestedPath,
		Reason: fmt.Sprintf("requested path %q targets a reserved shadow metadata namespace", relativePath),
	}
}

func joinWithinBase(basePath, relativePath string) (string, error) {
	candidatePath := filepath.Join(basePath, relativePath)
	if within, err := isWithinBase(basePath, candidatePath); err != nil {
		return "", err
	} else if !within {
		return "", &SecurityError{
			Field:  candidatePath,
			Reason: fmt.Sprintf("resolved path escapes base directory %q", basePath),
		}
	}

	return filepath.Clean(candidatePath), nil
}

func isWithinBase(basePath, candidatePath string) (bool, error) {
	baseAbs, err := filepath.Abs(filepath.Clean(basePath))
	if err != nil {
		return false, fmt.Errorf("normalize base path %q: %w", basePath, err)
	}

	candidateAbs, err := filepath.Abs(filepath.Clean(candidatePath))
	if err != nil {
		return false, fmt.Errorf("normalize candidate path %q: %w", candidatePath, err)
	}

	if filepath.VolumeName(baseAbs) != filepath.VolumeName(candidateAbs) {
		return false, nil
	}

	relativePath, err := filepath.Rel(baseAbs, candidateAbs)
	if err != nil {
		return false, fmt.Errorf("derive relative path from %q to %q: %w", baseAbs, candidateAbs, err)
	}
	if relativePath == "." {
		return true, nil
	}

	return !escapesBase(relativePath), nil
}

func relativeIfWithinBase(basePath, candidatePath string) (string, bool, error) {
	baseAbs, err := filepath.Abs(filepath.Clean(basePath))
	if err != nil {
		return "", false, fmt.Errorf("normalize base path %q: %w", basePath, err)
	}

	candidateAbs, err := filepath.Abs(filepath.Clean(candidatePath))
	if err != nil {
		return "", false, fmt.Errorf("normalize candidate path %q: %w", candidatePath, err)
	}

	if filepath.VolumeName(baseAbs) != filepath.VolumeName(candidateAbs) {
		return "", false, nil
	}

	relativePath, err := filepath.Rel(baseAbs, candidateAbs)
	if err != nil {
		return "", false, fmt.Errorf("derive relative path from %q to %q: %w", baseAbs, candidateAbs, err)
	}
	if relativePath == "." {
		return ".", true, nil
	}
	if escapesBase(relativePath) {
		return "", false, nil
	}

	return relativePath, true, nil
}

func samePath(leftPath, rightPath string) bool {
	leftRelative, ok, err := relativeIfWithinBase(leftPath, rightPath)
	if err != nil || !ok {
		return false
	}

	return leftRelative == "."
}

func escapesBase(relativePath string) bool {
	return relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) || filepath.IsAbs(relativePath)
}

func isReservedShadowRelativePath(relativePath string) bool {
	cleaned := filepath.Clean(relativePath)
	return cleaned == shadowMetadataDirName || strings.HasPrefix(cleaned, shadowMetadataDirName+string(filepath.Separator))
}

func tombstoneRootPath(shadowRoot string) string {
	return filepath.Join(shadowRoot, shadowMetadataDirName, shadowTombstonesDirName)
}

func tombstoneMarkerPath(shadowRoot, relativePath string) (string, error) {
	if relativePath == "." || relativePath == "" {
		return "", &SecurityError{
			Field:  relativePath,
			Reason: "refusing to create a tombstone for the shadow root",
		}
	}

	return joinWithinBase(tombstoneRootPath(shadowRoot), relativePath+tombstoneFileSuffix)
}

func hasTombstoneForRelativePath(shadowRoot, relativePath string) (bool, error) {
	for _, candidate := range relativePathAncestors(relativePath) {
		tombstonePath, err := tombstoneMarkerPath(shadowRoot, candidate)
		if err != nil {
			return false, err
		}
		if _, err := os.Stat(tombstonePath); err == nil {
			return true, nil
		} else if !os.IsNotExist(err) {
			return false, fmt.Errorf("stat tombstone %q: %w", tombstonePath, err)
		}
	}

	return false, nil
}

func relativePathAncestors(relativePath string) []string {
	cleaned := filepath.Clean(relativePath)
	if cleaned == "." || cleaned == "" {
		return nil
	}

	ancestors := make([]string, 0, 4)
	for current := cleaned; current != "." && current != ""; current = filepath.Dir(current) {
		ancestors = append(ancestors, current)

		next := filepath.Dir(current)
		if next == current || next == "." {
			break
		}
	}

	return ancestors
}

func applyTombstones(workspaceRoot, shadowRoot string) error {
	tombstoneRoot := tombstoneRootPath(shadowRoot)
	if _, err := os.Stat(tombstoneRoot); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat tombstone root %q: %w", tombstoneRoot, err)
	}

	return filepath.WalkDir(tombstoneRoot, func(currentPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk tombstone path %q: %w", currentPath, walkErr)
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return &SecurityError{
				Field:  currentPath,
				Reason: "tombstone entry must not be a symbolic link",
			}
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat tombstone entry %q: %w", currentPath, err)
		}
		if !info.Mode().IsRegular() {
			return &SecurityError{
				Field:  currentPath,
				Reason: fmt.Sprintf("tombstone entry must be a regular file, got %v", info.Mode()),
			}
		}

		relativeMarkerPath, err := filepath.Rel(tombstoneRoot, currentPath)
		if err != nil {
			return fmt.Errorf("derive tombstone relative path for %q: %w", currentPath, err)
		}
		if escapesBase(relativeMarkerPath) {
			return &SecurityError{
				Field:  currentPath,
				Reason: "tombstone path escapes tombstone root",
			}
		}
		if !strings.HasSuffix(relativeMarkerPath, tombstoneFileSuffix) {
			return &SecurityError{
				Field:  currentPath,
				Reason: "tombstone file has an invalid suffix",
			}
		}

		relativePath := strings.TrimSuffix(relativeMarkerPath, tombstoneFileSuffix)
		if relativePath == "." || relativePath == "" {
			return &SecurityError{
				Field:  currentPath,
				Reason: "refusing to delete the workspace root",
			}
		}

		targetPath, err := joinWithinBase(workspaceRoot, relativePath)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(targetPath); err != nil {
			return fmt.Errorf("apply tombstone to %q: %w", targetPath, err)
		}

		return nil
	})
}

func ensureTargetDirectory(targetPath string, mode os.FileMode) error {
	if existingInfo, err := os.Stat(targetPath); err == nil {
		if !existingInfo.IsDir() {
			return &SecurityError{
				Field:  targetPath,
				Reason: "workspace target exists as a non-directory",
			}
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat workspace directory %q: %w", targetPath, err)
	}

	if err := os.MkdirAll(targetPath, mode); err != nil {
		return fmt.Errorf("create workspace directory %q: %w", targetPath, err)
	}

	return nil
}

func copyFileIntoWorkspace(sourcePath, targetPath string, mode os.FileMode) error {
	if existingInfo, err := os.Stat(targetPath); err == nil {
		if !existingInfo.Mode().IsRegular() {
			return &SecurityError{
				Field:  targetPath,
				Reason: "workspace target exists as a non-regular file",
			}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat workspace target %q: %w", targetPath, err)
	}

	parentDir := filepath.Dir(targetPath)
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		return fmt.Errorf("create workspace parent directory %q: %w", parentDir, err)
	}

	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open shadow file %q: %w", sourcePath, err)
	}
	defer sourceFile.Close()

	tempFile, err := os.CreateTemp(parentDir, ".loom-commit-*")
	if err != nil {
		return fmt.Errorf("create temp file in %q: %w", parentDir, err)
	}

	tempPath := tempFile.Name()
	commitSucceeded := false
	defer func() {
		if commitSucceeded {
			return
		}
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
	}()

	if _, err := io.Copy(tempFile, sourceFile); err != nil {
		return fmt.Errorf("copy shadow file %q to temp file %q: %w", sourcePath, tempPath, err)
	}
	if err := tempFile.Chmod(mode); err != nil {
		return fmt.Errorf("set mode on temp file %q: %w", tempPath, err)
	}
	if err := tempFile.Sync(); err != nil {
		return fmt.Errorf("sync temp file %q: %w", tempPath, err)
	}
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("close temp file %q: %w", tempPath, err)
	}
	if err := replaceFile(tempPath, targetPath); err != nil {
		return err
	}

	commitSucceeded = true
	return nil
}

func replaceFile(sourcePath, targetPath string) error {
	if err := os.Rename(sourcePath, targetPath); err == nil {
		return nil
	}

	if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove existing target %q before replace: %w", targetPath, err)
	}
	if err := os.Rename(sourcePath, targetPath); err != nil {
		return fmt.Errorf("replace workspace target %q with %q: %w", targetPath, sourcePath, err)
	}

	return nil
}
