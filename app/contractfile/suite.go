package contractfile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"assay/backend"
)

// A suite is a folder of contract files with a suite.yaml that lists them.
// It is checked, published, and run as one contract whose rules are its
// contracts' rules, named <contract>/<rule>, so it passes only when every
// rule of every contract passes. Everything that takes a contract file
// takes a suite folder.

// SuiteFile names a folder's suite manifest.
const SuiteFile = "suite.yaml"

type fileSuite struct {
	Version   *int                `yaml:"version"`
	ID        string              `yaml:"id"`
	Name      string              `yaml:"name"`
	Contracts []string            `yaml:"contracts"`
	DependsOn map[string][]string `yaml:"depends_on"`
}

// suiteMember is one of a suite's contract files, kept for locating
// problems found after the suite was combined.
type suiteMember struct {
	name        string // the file's name without .yaml, and its rules' prefix
	root        *yaml.Node
	decodeLines map[int]bool
	offset      int // index of its first rule in the combined contract
	count       int
}

type suiteInfo struct {
	root    *yaml.Node
	members []suiteMember
}

// IsSuite reports whether path is a suite folder (or its suite.yaml).
func IsSuite(path string) bool {
	if filepath.Base(path) == SuiteFile {
		path = filepath.Dir(path)
	}
	info, err := os.Stat(filepath.Join(path, SuiteFile))
	return err == nil && info.Mode().IsRegular()
}

// SuiteContracts returns the contract file names a suite.yaml lists, as
// written, for copying a suite. Problems are left to CheckSuite.
func SuiteContracts(dir string) (id string, files []string, err error) {
	data, err := os.ReadFile(filepath.Join(dir, SuiteFile))
	if err != nil {
		return "", nil, err
	}
	var file fileSuite
	if err := yaml.Unmarshal(data, &file); err != nil {
		return "", nil, fmt.Errorf("%s: %v", SuiteFile, err)
	}
	for _, name := range file.Contracts {
		files = append(files, name+".yaml")
	}
	return file.ID, files, nil
}

// CheckSuite reads a suite folder, checks it and each of its contract
// files, and combines them into one contract. The error is non-nil only
// when suite.yaml can't be read.
func CheckSuite(dir string) (Result, error) {
	if filepath.Base(dir) == SuiteFile {
		dir = filepath.Dir(dir)
	}
	result := Result{Path: dir}
	data, err := os.ReadFile(filepath.Join(dir, SuiteFile))
	if err != nil {
		return result, err
	}
	add := func(p Problem) {
		if p.File == "" {
			p.File = SuiteFile
		}
		result.Problems = append(result.Problems, p)
	}

	root, extra, err := documents(data)
	if err != nil {
		add(yamlProblem(err))
		return result, nil
	}
	if root == nil || root.Kind != yaml.MappingNode {
		add(Problem{Message: "expected a suite: id, name, and the contracts it groups"})
		return result, nil
	}
	if extra != nil {
		add(Problem{Line: extra.Line, Column: extra.Column, Message: "a suite.yaml holds one suite; found a second YAML document"})
	}
	var file fileSuite
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		var typeErr *yaml.TypeError
		if !errors.As(err, &typeErr) {
			add(yamlProblem(err))
			return sorted(result), nil
		}
		for _, message := range typeErr.Errors {
			add(yamlProblem(errors.New(message)))
		}
		return sorted(result), nil
	}
	at := func(field string) (int, int) { return position(root, -1, field) }
	if file.Version != nil && *file.Version != FormatVersion {
		line, column := at("version")
		add(Problem{Line: line, Column: column, Message: fmt.Sprintf("unsupported file version %d; this version of assay reads version %d", *file.Version, FormatVersion)})
	}
	if strings.TrimSpace(file.ID) == "" {
		line, column := at("id")
		add(Problem{Line: line, Column: column, Message: "suite ID is required"})
	}
	if strings.TrimSpace(file.Name) == "" {
		line, column := at("name")
		add(Problem{Line: line, Column: column, Message: "suite name is required"})
	}
	if len(file.Contracts) == 0 {
		line, column := at("contracts")
		add(Problem{Line: line, Column: column, Message: "a suite needs at least one contract: list the contract files (without .yaml) under contracts"})
	}

	// Each listed contract, where it's written in suite.yaml.
	_, list := lookup(root, "contracts")
	itemAt := func(i int) (int, int) {
		if list != nil && list.Kind == yaml.SequenceNode && i < len(list.Content) {
			return list.Content[i].Line, list.Content[i].Column
		}
		return at("contracts")
	}
	listed := map[string]bool{}
	var members []suiteMember
	var contracts []backend.Contract
	for i, name := range file.Contracts {
		line, column := itemAt(i)
		switch {
		case !backend.CommandName(name):
			add(Problem{Line: line, Column: column, Message: fmt.Sprintf("%q isn't a contract file name (letters, digits, '.', '_', '-'; without .yaml)", name)})
			continue
		case listed[name]:
			add(Problem{Line: line, Column: column, Message: fmt.Sprintf("%s is listed twice", name)})
			continue
		}
		listed[name] = true
		memberFile := name + ".yaml"
		memberData, err := os.ReadFile(filepath.Join(dir, memberFile))
		if err != nil {
			message := fmt.Sprintf("can't read %s: %v", memberFile, err)
			if errors.Is(err, os.ErrNotExist) {
				message = fmt.Sprintf("there's no %s in the suite's folder", memberFile)
			}
			add(Problem{Line: line, Column: column, Message: message})
			continue
		}
		member := Parse(memberData)
		for _, p := range member.Problems {
			p.File = memberFile
			result.Problems = append(result.Problems, p)
		}
		if member.Valid() && member.Contract.ID != name {
			line, column := position(member.root, -1, "id")
			result.Problems = append(result.Problems, Problem{File: memberFile, Line: line, Column: column, Message: fmt.Sprintf("in a suite, a contract's id must match its file name: %q", name)})
		}
		members = append(members, suiteMember{name: name, root: member.root, decodeLines: member.decodeLines, count: len(member.Contract.Rules)})
		contracts = append(contracts, member.Contract)
	}

	// Dependencies between contracts, reported where they're written.
	_, deps := lookup(root, "depends_on")
	depAt := func(name string) (int, int) {
		if key, _ := lookup(deps, name); key != nil {
			return key.Line, key.Column
		}
		return at("depends_on")
	}
	for name, on := range file.DependsOn {
		line, column := depAt(name)
		if !listed[name] {
			add(Problem{Line: line, Column: column, Message: fmt.Sprintf("depends_on names %s, which isn't in contracts", name)})
		}
		for _, dependency := range on {
			if !listed[dependency] {
				add(Problem{Line: line, Column: column, Message: fmt.Sprintf("%s depends on %s, which isn't in contracts", name, dependency)})
			}
		}
	}
	if cycle := contractCycle(file.Contracts, file.DependsOn); cycle != "" {
		line, column := at("depends_on")
		add(Problem{Line: line, Column: column, Message: "dependency cycle " + cycle})
	}
	if !result.Valid() {
		return sorted(result), nil
	}

	// Combine: each rule becomes <contract>/<rule>, keeps its own
	// dependencies, and depends on every rule of the contracts its
	// contract depends on.
	combined := backend.Contract{ID: file.ID, Name: file.Name}
	ruleIDs := map[string][]string{}
	for _, c := range contracts {
		for _, rule := range c.Rules {
			ruleIDs[c.ID] = append(ruleIDs[c.ID], c.ID+"/"+rule.ID)
		}
	}
	for i, c := range contracts {
		members[i].offset = len(combined.Rules)
		for _, rule := range c.Rules {
			rule.ID = c.ID + "/" + rule.ID
			var dependsOn []string
			for _, dependency := range rule.DependsOn {
				dependsOn = append(dependsOn, c.ID+"/"+dependency)
			}
			for _, other := range file.DependsOn[c.ID] {
				dependsOn = append(dependsOn, ruleIDs[other]...)
			}
			rule.DependsOn = dependsOn
			combined.Rules = append(combined.Rules, rule)
		}
	}
	result.Contract = combined
	result.Members = file.Contracts
	result.suite = &suiteInfo{root: root, members: members}
	result.AddValidation(backend.ValidateContract(combined))
	return result, nil
}

// contractCycle finds a cycle among a suite's contract dependencies and
// describes it, or returns "".
func contractCycle(names []string, dependsOn map[string][]string) string {
	const (
		unvisited = iota
		visiting
		done
	)
	state := map[string]int{}
	var path []string
	var visit func(string) string
	visit = func(name string) string {
		switch state[name] {
		case visiting:
			start := 0
			for i, n := range path {
				if n == name {
					start = i
				}
			}
			return strings.Join(append(append([]string{}, path[start:]...), name), " → ")
		case done:
			return ""
		}
		state[name] = visiting
		path = append(path, name)
		for _, next := range dependsOn[name] {
			if cycle := visit(next); cycle != "" {
				return cycle
			}
		}
		path = path[:len(path)-1]
		state[name] = done
		return ""
	}
	for _, name := range names {
		if cycle := visit(name); cycle != "" {
			return cycle
		}
	}
	return ""
}

// suiteProblem locates a validation error of a combined suite contract in
// the contract file its rule came from, or in suite.yaml.
func (s *suiteInfo) problem(v *backend.ValidationError) (Problem, bool) {
	if v.Rule < 0 {
		line, column := position(s.root, -1, v.Field)
		return Problem{File: SuiteFile, Line: line, Column: column, Message: v.Message}, true
	}
	for _, m := range s.members {
		if v.Rule >= m.offset && v.Rule < m.offset+m.count {
			line, column := position(m.root, v.Rule-m.offset, v.Field)
			if m.decodeLines[line] {
				return Problem{}, false
			}
			return Problem{File: m.name + ".yaml", Line: line, Column: column, RuleID: strings.TrimPrefix(v.RuleID, m.name+"/"), Message: v.Message}, true
		}
	}
	return Problem{File: SuiteFile, Message: v.Message}, true
}
