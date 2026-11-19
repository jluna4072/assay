// Package contractfile reads contracts written as YAML files. Decoding is
// strict: unknown keys, duplicate keys, and wrong types are problems with line
// numbers, so a typo can't silently drop part of a contract.
package contractfile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"harness/backend"
)

// FormatVersion is the only contract file format this build understands.
const FormatVersion = 1

type fileContract struct {
	Version *int       `yaml:"version"`
	ID      string     `yaml:"id"`
	Name    string     `yaml:"name"`
	Rules   []fileRule `yaml:"rules"`
}

type fileRule struct {
	ID        string   `yaml:"id"`
	Criterion string   `yaml:"criterion"`
	Check     string   `yaml:"check"`
	Path      string   `yaml:"path"`
	DependsOn []string `yaml:"depends_on"`
}

// Problem is one reason a contract file is invalid. Line and Column are
// 1-based and zero when unknown.
type Problem struct {
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	RuleID  string `json:"rule,omitempty"`
	Message string `json:"message"`
}

func (p Problem) String() string {
	location := ""
	switch {
	case p.Line > 0 && p.Column > 0:
		location = fmt.Sprintf("%d:%d: ", p.Line, p.Column)
	case p.Line > 0:
		location = fmt.Sprintf("%d: ", p.Line)
	}
	if p.RuleID != "" {
		return fmt.Sprintf("%srule %q: %s", location, p.RuleID, p.Message)
	}
	return location + p.Message
}

// Result is a parsed and validated contract file.
type Result struct {
	Path     string
	Contract backend.Contract
	Problems []Problem
}

func (r Result) Valid() bool { return len(r.Problems) == 0 }

// InvalidError is returned by Load when a file has problems.
type InvalidError struct {
	Path     string
	Problems []Problem
}

func (e *InvalidError) Error() string {
	lines := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		lines[i] = e.Path + ":" + p.String()
	}
	return strings.Join(lines, "\n")
}

// Load reads a contract file and returns it only if it is valid for publication.
func Load(path string) (backend.Contract, error) {
	result, err := Check(path)
	if err != nil {
		return backend.Contract{}, err
	}
	if !result.Valid() {
		return result.Contract, &InvalidError{Path: path, Problems: result.Problems}
	}
	return result.Contract, nil
}

// Check reads, decodes, and validates a contract file. The error is non-nil
// only when the file can't be read; contract problems are in the Result.
func Check(path string) (Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{Path: path}, err
	}
	result := Parse(data)
	result.Path = path
	return result, nil
}

// Parse decodes and validates contract YAML.
func Parse(data []byte) Result {
	var result Result
	add := func(p Problem) { result.Problems = append(result.Problems, p) }

	root, extra, err := documents(data)
	if err != nil {
		add(yamlProblem(err))
		return result
	}
	if root == nil {
		add(Problem{Message: "file is empty; expected a contract"})
		return result
	}
	if extra != nil {
		add(Problem{Line: extra.Line, Column: extra.Column, Message: "a file holds one contract; found a second YAML document"})
	}
	if root.Kind != yaml.MappingNode {
		add(Problem{Line: root.Line, Column: root.Column, Message: "expected a contract mapping with id, name, and rules"})
		return result
	}

	var file fileContract
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	// Lines that already have a decoding problem. A field that failed to
	// decode is left empty, so validation would only repeat the same problem.
	decodeLines := map[int]bool{}
	if err := decoder.Decode(&file); err != nil {
		// A TypeError still decodes every field it can, so validation below
		// can report the remaining problems in the same pass.
		var typeErr *yaml.TypeError
		if !errors.As(err, &typeErr) {
			add(yamlProblem(err))
			return result
		}
		for _, message := range typeErr.Errors {
			problem := yamlProblem(errors.New(message))
			add(problem)
			decodeLines[problem.Line] = true
			if strings.Contains(message, "already defined") {
				// A duplicate key stops decoding of its whole mapping, so
				// validating the rest would report fields that are present.
				return sorted(result)
			}
		}
	}
	if line, column := position(root, -1, "version"); file.Version != nil && *file.Version != FormatVersion && !decodeLines[line] {
		add(Problem{Line: line, Column: column, Message: fmt.Sprintf("unsupported file version %d; this harness reads version %d", *file.Version, FormatVersion)})
	}

	result.Contract = backend.Contract{ID: file.ID, Name: file.Name, Rules: make([]backend.Rule, len(file.Rules))}
	for i, rule := range file.Rules {
		result.Contract.Rules[i] = backend.Rule{
			ID: rule.ID, Criterion: rule.Criterion, Kind: backend.RuleKind(rule.Check),
			Path: rule.Path, DependsOn: rule.DependsOn,
		}
	}
	for _, v := range backend.ValidationErrors(backend.ValidateContract(result.Contract)) {
		line, column := position(root, v.Rule, v.Field)
		if decodeLines[line] {
			continue
		}
		add(Problem{Line: line, Column: column, RuleID: v.RuleID, Message: v.Message})
	}
	return sorted(result)
}

func sorted(result Result) Result {
	sort.SliceStable(result.Problems, func(i, j int) bool {
		return result.Problems[i].Line < result.Problems[j].Line
	})
	return result
}

// documents returns the first YAML document's root node and, if present, the
// root of a second document.
func documents(data []byte) (root, extra *yaml.Node, err error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var first yaml.Node
	if err := decoder.Decode(&first); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	root = &first
	if first.Kind == yaml.DocumentNode && len(first.Content) > 0 {
		root = first.Content[0]
	}
	var second yaml.Node
	if err := decoder.Decode(&second); err == nil {
		extra = &second
		if second.Kind == yaml.DocumentNode && len(second.Content) > 0 {
			extra = second.Content[0]
		}
	} else if !errors.Is(err, io.EOF) {
		return nil, nil, err
	}
	return root, extra, nil
}

// position finds where a contract field (rule < 0) or a rule's field is
// written, falling back to the enclosing rule, then the rules list.
func position(root *yaml.Node, rule int, field string) (line, column int) {
	if rule < 0 {
		if key, _ := lookup(root, field); key != nil {
			return key.Line, key.Column
		}
		return 0, 0
	}
	key, rules := lookup(root, "rules")
	if rules == nil || rules.Kind != yaml.SequenceNode || rule >= len(rules.Content) {
		if key != nil {
			return key.Line, key.Column
		}
		return 0, 0
	}
	item := rules.Content[rule]
	if fieldKey, _ := lookup(item, field); fieldKey != nil {
		return fieldKey.Line, fieldKey.Column
	}
	return item.Line, item.Column
}

func lookup(mapping *yaml.Node, key string) (keyNode, value *yaml.Node) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i], mapping.Content[i+1]
		}
	}
	return nil, nil
}

var (
	lineMessage  = regexp.MustCompile(`^(?:yaml: )?line (\d+): (.*)$`)
	unknownField = regexp.MustCompile(`^field (\S+) not found in type `)
	wrongType    = regexp.MustCompile("^cannot unmarshal !!(\\w+)(?: `[^`]*`)? into (\\S+)$")
)

// yamlProblem turns a YAML library message into a Problem with a line number
// and wording that names YAML concepts rather than Go types.
func yamlProblem(err error) Problem {
	message := strings.TrimPrefix(err.Error(), "yaml: ")
	problem := Problem{Message: message}
	if match := lineMessage.FindStringSubmatch(message); match != nil {
		problem.Line, _ = strconv.Atoi(match[1])
		problem.Message = match[2]
	}
	if match := unknownField.FindStringSubmatch(problem.Message); match != nil {
		problem.Message = fmt.Sprintf("unknown key %q", match[1])
	} else if match := wrongType.FindStringSubmatch(problem.Message); match != nil {
		problem.Message = fmt.Sprintf("expected %s, found %s", describeTarget(match[2]), describeTag(match[1]))
	}
	return problem
}

func describeTarget(goType string) string {
	switch {
	case strings.HasPrefix(goType, "[]"):
		return "a list"
	case goType == "string":
		return "text"
	case goType == "int" || goType == "*int":
		return "a whole number"
	default:
		return "a mapping"
	}
}

func describeTag(tag string) string {
	switch tag {
	case "seq":
		return "a list"
	case "map":
		return "a mapping"
	case "str":
		return "text"
	case "int", "float":
		return "a number"
	case "bool":
		return "true/false"
	case "null":
		return "nothing"
	default:
		return tag
	}
}
