// Package project locates a harness project: a directory holding a .harness
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
	StateDir     = ".harness"     // local state, gitignored
	DatabaseFile = "harness.db"   // inside StateDir
	ContractsDir = "harness"      // contract files, committed
	ExampleFile  = "example.yaml" // written into ContractsDir by Init
)

// ErrNotFound means no directory from the start up to the filesystem root
// holds a .harness directory.
var ErrNotFound = errors.New("no harness project found")

// Project is a located harness project.
type Project struct {
	Root string // absolute path of the directory containing .harness
}

func (p Project) StatePath() string     { return filepath.Join(p.Root, StateDir) }
func (p Project) DatabasePath() string  { return filepath.Join(p.Root, StateDir, DatabaseFile) }
func (p Project) ContractsPath() string { return filepath.Join(p.Root, ContractsDir) }

// Find walks up from start to the nearest directory containing .harness.
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

// EnsureIgnored adds .harness/ to dir/.gitignore unless an equivalent entry
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
