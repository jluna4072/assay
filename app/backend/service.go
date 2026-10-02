package backend

import (
	"errors"
	"fmt"
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

// Service is shared by desktop and future MCP callers; it has no UI dependency.
type Service struct {
	store Repository
	mu    sync.Mutex
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
