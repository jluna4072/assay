package backend

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

var ErrNotFound = errors.New("not found")

type PublishedContract struct {
	Contract    Contract  `json:"contract"`
	Version     int       `json:"version"`
	PublishedAt time.Time `json:"publishedAt"`
}

// Repository owns draft storage and atomically allocates immutable versions.
// Implementations must return independent snapshots, not internal references.
type Repository interface {
	SaveDraft(Contract) error
	GetDraft(string) (Contract, error)
	ListContracts() ([]Contract, error)
	CreateVersion(Contract, time.Time) (PublishedContract, error)
	GetVersion(string, int) (PublishedContract, error)
	ListVersions(string) ([]PublishedContract, error)
}

// Service is shared by the command line and future MCP callers; it has no UI dependency.
type Service struct {
	store Repository
	mu    sync.Mutex
	// LoadConfig reads a project's local config when an attempt runs an
	// approved command. Without it, no commands are approved.
	LoadConfig func(projectDir string) (Config, error)
}

func NewService(store Repository) *Service { return &Service{store: store} }

func (s *Service) SaveDraft(contract Contract) (Contract, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(contract.ID) == "" {
		return Contract{}, fmt.Errorf("draft ID is required")
	}
	if err := s.store.SaveDraft(contract); err != nil {
		return Contract{}, err
	}
	return s.store.GetDraft(contract.ID)
}

func (s *Service) GetDraft(id string) (Contract, error) { return s.store.GetDraft(id) }

func (s *Service) ListContracts() ([]Contract, error) { return s.store.ListContracts() }

func (s *Service) PublishContract(id string) (PublishedContract, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	contract, err := s.store.GetDraft(id)
	if err != nil {
		return PublishedContract{}, err
	}
	if err := ValidateContract(contract); err != nil {
		return PublishedContract{}, err
	}
	return s.store.CreateVersion(contract, time.Now().UTC())
}

func (s *Service) ListVersions(id string) ([]PublishedContract, error) {
	return s.store.ListVersions(id)
}

func (s *Service) GetVersion(id string, version int) (PublishedContract, error) {
	return s.store.GetVersion(id, version)
}

// PublishContent validates a complete contract (for example, one read from a
// file) and publishes it as the next version. If it matches the latest
// published version, nothing is written and changed is false.
func (s *Service) PublishContent(contract Contract) (published PublishedContract, changed bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ValidateContract(contract); err != nil {
		return PublishedContract{}, false, err
	}
	versions, err := s.store.ListVersions(contract.ID)
	if err != nil {
		return PublishedContract{}, false, err
	}
	if n := len(versions); n > 0 && SameContent(versions[n-1].Contract, contract) {
		return versions[n-1], false, nil
	}
	if err := s.store.SaveDraft(contract); err != nil {
		return PublishedContract{}, false, err
	}
	published, err = s.store.CreateVersion(contract, time.Now().UTC())
	return published, err == nil, err
}

// SameContent reports whether two contracts would be checked identically.
// An empty dependency list and a missing one are the same.
func SameContent(a, b Contract) bool {
	if a.ID != b.ID || a.Name != b.Name || len(a.Rules) != len(b.Rules) {
		return false
	}
	for i := range a.Rules {
		x, y := a.Rules[i], b.Rules[i]
		if x.ID != y.ID || x.Criterion != y.Criterion || x.Kind != y.Kind || x.Path != y.Path || x.Command != y.Command || x.Reviewer != y.Reviewer || !slices.Equal(x.DependsOn, y.DependsOn) ||
			!slices.EqualFunc(x.Evidence, y.Evidence, func(a, b EvidenceSpec) bool {
				return a.Kind == b.Kind && a.Path == b.Path && a.Command == b.Command && slices.Equal(a.Paths, b.Paths)
			}) {
			return false
		}
	}
	return true
}
