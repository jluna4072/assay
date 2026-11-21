// Package project locates an assay project: a directory holding a .assay
// state directory, found by walking up from the working directory like git.
package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	StateDir     = ".assay"       // local state, gitignored
	DatabaseFile = "assay.db"     // inside StateDir
	ContractsDir = "assay"        // contract files, committed
	ExampleFile  = "example.yaml" // written into ContractsDir by Init
)

// ErrNotFound means no directory from the start up to the filesystem root
// holds a .assay directory.
var ErrNotFound = errors.New("no assay project found")

// Project is a located assay project.
type Project struct {
	Root string // absolute path of the directory containing .assay
}

func (p Project) StatePath() string     { return filepath.Join(p.Root, StateDir) }
func (p Project) DatabasePath() string  { return filepath.Join(p.Root, StateDir, DatabaseFile) }
func (p Project) ContractsPath() string { return filepath.Join(p.Root, ContractsDir) }

// Find walks up from start to the nearest directory containing .assay.
func Find(start string) (Project, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return Project{}, err
	}
	for {
		info, err := os.Stat(filepath.Join(dir, StateDir))
		if err == nil && info.IsDir() {
			return Project{Root: dir}, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Project{}, err
		}
		if HasLegacy(dir) {
			return Project{}, fmt.Errorf("%s: %w", dir, ErrLegacy)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return Project{}, fmt.Errorf("%w in %s or any parent directory", ErrNotFound, start)
		}
		dir = parent
	}
}

// Open returns the project rooted exactly at dir, without walking up.
func Open(dir string) (Project, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return Project{}, err
	}
	if HasLegacy(root) {
		return Project{}, fmt.Errorf("%s: %w", root, ErrLegacy)
	}
	info, err := os.Stat(filepath.Join(root, StateDir))
	if errors.Is(err, os.ErrNotExist) || (err == nil && !info.IsDir()) {
		return Project{}, fmt.Errorf("%w in %s", ErrNotFound, dir)
	}
	if err != nil {
		return Project{}, err
	}
	return Project{Root: root}, nil
}

// InGitRepo reports whether dir is inside a git working tree.
func InGitRepo(dir string) bool {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// EnsureIgnored adds .assay/ to dir/.gitignore unless an equivalent entry
// is already there. It reports whether the file changed.
func EnsureIgnored(dir string) (bool, error) {
	path := filepath.Join(dir, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		switch strings.TrimSpace(line) {
		case StateDir, StateDir + "/", "/" + StateDir, "/" + StateDir + "/":
			return false, nil
		}
	}
	addition := "/" + StateDir + "/\n"
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		addition = "\n" + addition
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return false, err
	}
	if _, err := file.WriteString(addition); err != nil {
		file.Close()
		return false, err
	}
	return true, file.Close()
}

// Names used before the tool was renamed from harness to assay.
const (
	LegacyStateDir     = ".harness"
	LegacyDatabaseFile = "harness.db"
	LegacyContractsDir = "harness"
)

// ErrLegacy means a directory holds a project made before the rename,
// which "assay init" moves over.
var ErrLegacy = errors.New("this project was made before harness was renamed to assay; run \"assay init\" here to move it over")

// HasLegacy reports whether dir holds a pre-rename project and no current one.
func HasLegacy(dir string) bool {
	if info, err := os.Stat(filepath.Join(dir, StateDir)); err == nil && info.IsDir() {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, LegacyStateDir))
	return err == nil && info.IsDir()
}

// MigrateLegacy moves a pre-rename project at root to the current names:
// .harness/ becomes .assay/ (with harness.db renamed to assay.db), the
// harness/ contracts folder becomes assay/ when there's no assay/ folder
// yet, and the .gitignore entry follows. It returns what it moved.
// Callers update paths stored in the database and repair git worktrees.
func MigrateLegacy(root string) ([]string, error) {
	var moved []string
	if err := os.Rename(filepath.Join(root, LegacyStateDir), filepath.Join(root, StateDir)); err != nil {
		return moved, err
	}
	moved = append(moved, LegacyStateDir+"/ → "+StateDir+"/")
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		from := filepath.Join(root, StateDir, LegacyDatabaseFile+suffix)
		if _, err := os.Stat(from); err == nil {
			if err := os.Rename(from, filepath.Join(root, StateDir, DatabaseFile+suffix)); err != nil {
				return moved, err
			}
		}
	}
	legacyContracts, contracts := filepath.Join(root, LegacyContractsDir), filepath.Join(root, ContractsDir)
	if info, err := os.Stat(legacyContracts); err == nil && info.IsDir() {
		if _, err := os.Stat(contracts); errors.Is(err, os.ErrNotExist) {
			if err := os.Rename(legacyContracts, contracts); err != nil {
				return moved, err
			}
			moved = append(moved, LegacyContractsDir+"/ → "+ContractsDir+"/")
		}
	}
	path := filepath.Join(root, ".gitignore")
	if data, err := os.ReadFile(path); err == nil {
		lines := strings.Split(string(data), "\n")
		changed := false
		for i, line := range lines {
			switch strings.TrimSpace(line) {
			case LegacyStateDir, LegacyStateDir + "/", "/" + LegacyStateDir, "/" + LegacyStateDir + "/":
				lines[i] = "/" + StateDir + "/"
				changed = true
			}
		}
		if changed {
			if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644); err != nil {
				return moved, err
			}
			moved = append(moved, ".gitignore: /"+LegacyStateDir+"/ → /"+StateDir+"/")
		}
	}
	return moved, nil
}
