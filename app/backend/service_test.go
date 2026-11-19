package backend

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// JSON snapshots model the repository boundary without a database dependency.
type memoryRepository struct {
	drafts   map[string][]byte
	versions map[string][][]byte
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{drafts: map[string][]byte{}, versions: map[string][][]byte{}}
}

func (m *memoryRepository) SaveDraft(c Contract) error {
	data, err := json.Marshal(c)
	m.drafts[c.ID] = data
	return err
}

func (m *memoryRepository) GetDraft(id string) (c Contract, err error) {
	data, ok := m.drafts[id]
	if !ok {
		return c, ErrNotFound
	}
	err = json.Unmarshal(data, &c)
	return
}

func (m *memoryRepository) ListContracts() ([]Contract, error) {
	result := []Contract{}
	for id := range m.drafts {
		c, _ := m.GetDraft(id)
		result = append(result, c)
	}
	return result, nil
}

func (m *memoryRepository) CreateVersion(c Contract, at time.Time) (PublishedContract, error) {
	v := PublishedContract{Contract: c, Version: len(m.versions[c.ID]) + 1, PublishedAt: at}
	data, err := json.Marshal(v)
	if err != nil {
		return PublishedContract{}, err
	}
	m.versions[c.ID] = append(m.versions[c.ID], data)
	return m.GetVersion(c.ID, v.Version)
}

func (m *memoryRepository) GetVersion(id string, version int) (v PublishedContract, err error) {
	if version < 1 || version > len(m.versions[id]) {
		return v, ErrNotFound
	}
	err = json.Unmarshal(m.versions[id][version-1], &v)
	return
}

func (m *memoryRepository) ListVersions(id string) ([]PublishedContract, error) {
	result := []PublishedContract{}
	for version := range m.versions[id] {
		v, _ := m.GetVersion(id, version+1)
		result = append(result, v)
	}
	return result, nil
}

func validContract() Contract {
	return Contract{ID: "docs", Name: "Documentation", Rules: []Rule{
		{ID: "readme", Criterion: "Project has a README", Kind: FileExists, Path: "README.md"},
		{ID: "guide", Criterion: "Project has a guide", Kind: FileExists, Path: "docs/guide.md", DependsOn: []string{"readme"}},
	}}
}

func TestPublicationFreezesDraft(t *testing.T) {
	s := NewService(newMemoryRepository())
	draft := validContract()
	if _, err := s.SaveDraft(draft); err != nil {
		t.Fatal(err)
	}
	first, err := s.PublishContract(draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := first
	draft.Name = "Changed"
	draft.Rules[1].DependsOn[0] = "guide"
	if _, err := s.SaveDraft(draft); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublishContract(draft.ID); err == nil {
		t.Fatal("cyclic draft was published")
	}
	first.Contract.Rules[1].DependsOn[0] = "tampered"
	stored, err := s.GetVersion(draft.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Contract.Name != "Documentation" || stored.Contract.Rules[1].DependsOn[0] != "readme" {
		t.Fatalf("published version changed: %+v", stored)
	}
	draft.Rules[1].DependsOn[0] = "readme"
	if _, err := s.SaveDraft(draft); err != nil {
		t.Fatal(err)
	}
	second, err := s.PublishContract(draft.ID)
	if err != nil || second.Version != 2 || second.Contract.Name != "Changed" {
		t.Fatalf("second version: %+v, %v", second, err)
	}
	versions, _ := s.ListVersions(draft.ID)
	if len(versions) != 2 || versions[0].PublishedAt != snapshot.PublishedAt {
		t.Fatalf("unexpected versions: %+v", versions)
	}
}

func TestPublicationValidation(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Contract)
	}{
		{"missing ID", func(c *Contract) { c.ID = " " }},
		{"missing name", func(c *Contract) { c.Name = "" }},
		{"no rules", func(c *Contract) { c.Rules = nil }},
		{"missing rule ID", func(c *Contract) { c.Rules[0].ID = "" }},
		{"missing criterion", func(c *Contract) { c.Rules[0].Criterion = " " }},
		{"duplicate IDs", func(c *Contract) { c.Rules[1].ID = c.Rules[0].ID }},
		{"missing dependency", func(c *Contract) { c.Rules[1].DependsOn = []string{"unknown"} }},
		{"duplicate dependency", func(c *Contract) { c.Rules[1].DependsOn = []string{"readme", "readme"} }},
		{"self cycle", func(c *Contract) { c.Rules[0].DependsOn = []string{"readme"} }},
		{"cycle", func(c *Contract) { c.Rules[0].DependsOn = []string{"guide"} }},
		{"unsupported kind", func(c *Contract) { c.Rules[0].Kind = "command" }},
		{"missing path", func(c *Contract) { c.Rules[0].Path = "" }},
		{"absolute path", func(c *Contract) { c.Rules[0].Path = "/etc/passwd" }},
		{"escaping path", func(c *Contract) { c.Rules[0].Path = "../outside" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validContract()
			tc.change(&c)
			if err := ValidateContract(c); err == nil {
				t.Fatal("invalid contract accepted")
			}
		})
	}
	c := validContract()
	c.Rules[0], c.Rules[1] = c.Rules[1], c.Rules[0]
	if err := ValidateContract(c); err != nil {
		t.Fatal("valid out-of-order graph:", err)
	}
}

func TestIncompleteDraftCanBeSavedButNotPublished(t *testing.T) {
	s := NewService(newMemoryRepository())
	draft := Contract{ID: "new"}
	if saved, err := s.SaveDraft(draft); err != nil || !reflect.DeepEqual(saved, draft) {
		t.Fatalf("save: %+v, %v", saved, err)
	}
	if _, err := s.PublishContract("new"); err == nil {
		t.Fatal("incomplete draft published")
	}
	if versions, _ := s.ListVersions("new"); len(versions) != 0 {
		t.Fatal("invalid version persisted")
	}
	if _, err := s.PublishContract("missing"); err != ErrNotFound {
		t.Fatal(fmt.Sprint("missing draft: ", err))
	}
}

func TestValidationReportsEveryProblem(t *testing.T) {
	c := validContract()
	c.Name = ""
	c.Rules[0].Kind = "command"
	c.Rules[0].DependsOn = []string{"guide"}
	c.Rules = append(c.Rules, Rule{ID: "guide", Criterion: "again", Kind: FileExists, Path: "x"})
	problems := ValidationErrors(ValidateContract(c))
	got := map[string]bool{}
	for _, p := range problems {
		got[fmt.Sprintf("%d/%s/%s", p.Rule, p.RuleID, p.Field)] = true
	}
	for _, want := range []string{"-1//name", "0/readme/check", "2/guide/id", "1/guide/depends_on"} {
		if !got[want] {
			t.Errorf("missing problem %s in %v", want, problems)
		}
	}
	for _, p := range problems {
		if p.Field == "depends_on" && !strings.Contains(p.Message, "readme → guide → readme") && !strings.Contains(p.Message, "guide → readme → guide") {
			t.Errorf("cycle message lacks path: %s", p.Message)
		}
	}
}

func TestPublishContentSkipsUnchangedVersions(t *testing.T) {
	s := NewService(newMemoryRepository())
	c := validContract()
	first, changed, err := s.PublishContent(c)
	if err != nil || !changed || first.Version != 1 {
		t.Fatalf("first: %+v %v %v", first, changed, err)
	}
	c.Rules[0].DependsOn = []string{} // same as no dependencies
	same, changed, err := s.PublishContent(c)
	if err != nil || changed || same.Version != 1 {
		t.Fatalf("unchanged: %+v %v %v", same, changed, err)
	}
	c.Rules[0].Path = "README.txt"
	second, changed, err := s.PublishContent(c)
	if err != nil || !changed || second.Version != 2 {
		t.Fatalf("changed: %+v %v %v", second, changed, err)
	}
	c.Rules[0].Path = "../outside"
	if _, _, err := s.PublishContent(c); err == nil {
		t.Fatal("invalid contract published")
	}
	if versions, _ := s.ListVersions(c.ID); len(versions) != 2 {
		t.Fatalf("versions: %d", len(versions))
	}
}
