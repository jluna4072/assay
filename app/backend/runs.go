package backend

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type ResultStatus string

const (
	Passed           ResultStatus = "passed"
	Failed           ResultStatus = "failed"
	OperationalError ResultStatus = "error"
	Blocked          ResultStatus = "blocked"
)

type Attempt struct {
	Number      int          `json:"number"`
	StartedAt   time.Time    `json:"startedAt"`
	CompletedAt time.Time    `json:"completedAt"`
	Status      ResultStatus `json:"status"`
	Results     []RuleResult `json:"results"`
}
type Run struct {
	ID         string    `json:"id"`
	ContractID string    `json:"contractId"`
	Version    int       `json:"version"`
	ProjectDir string    `json:"projectDir"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"createdAt"`
	Attempts   []Attempt `json:"attempts"`
}
type RunRepository interface {
	Repository
	CreateRun(Run) error
	GetRun(string) (Run, error)
	ListRuns() ([]Run, error)
	// AppendAttempt atomically numbers an attempt, saves results, and updates the run.
	AppendAttempt(string, Attempt) (Run, error)
}

func (s *Service) runRepository() (RunRepository, error) {
	store, ok := s.store.(RunRepository)
	if !ok {
		return nil, fmt.Errorf("repository does not support runs")
	}
	return store, nil
}
func (s *Service) StartRun(contractID string, version int, projectDir string) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	store, err := s.runRepository()
	if err != nil {
		return Run{}, err
	}
	if _, err := store.GetVersion(contractID, version); err != nil {
		return Run{}, err
	}
	if projectDir == "" {
		return Run{}, fmt.Errorf("project directory is required")
	}
	path, err := filepath.Abs(projectDir)
	if err != nil {
		return Run{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Run{}, fmt.Errorf("open project: %w", err)
	}
	if !info.IsDir() {
		return Run{}, fmt.Errorf("project must be a directory")
	}
	run := Run{ID: rand.Text(), ContractID: contractID, Version: version, ProjectDir: path, Status: "ready", CreatedAt: time.Now().UTC(), Attempts: []Attempt{}}
	if err := store.CreateRun(run); err != nil {
		return Run{}, err
	}
	return store.GetRun(run.ID)
}
func (s *Service) GetRun(id string) (Run, error) {
	store, err := s.runRepository()
	if err != nil {
		return Run{}, err
	}
	return store.GetRun(id)
}
func (s *Service) ListRuns() ([]Run, error) {
	store, err := s.runRepository()
	if err != nil {
		return nil, err
	}
	return store.ListRuns()
}
func (s *Service) EvaluateAttempt(runID string) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	store, err := s.runRepository()
	if err != nil {
		return Run{}, err
	}
	run, err := store.GetRun(runID)
	if err != nil {
		return Run{}, err
	}
	published, err := store.GetVersion(run.ContractID, run.Version)
	if err != nil {
		return Run{}, err
	}
	if err := ValidateContract(published.Contract); err != nil {
		return Run{}, err
	}
	attempt := Attempt{StartedAt: time.Now().UTC(), Status: Passed}
	rules := make(map[string]Rule)
	for _, rule := range published.Contract.Rules {
		rules[rule.ID] = rule
	}
	results := make(map[string]RuleResult)
	var evaluate func(string) RuleResult
	evaluate = func(id string) RuleResult {
		if result, ok := results[id]; ok {
			return result
		}
		rule := rules[id]
		for _, dependency := range rule.DependsOn {
			if result := evaluate(dependency); result.Status != Passed {
				result := RuleResult{RuleID: id, Status: Blocked, Reason: fmt.Sprintf("dependency %s did not pass", dependency)}
				results[id] = result
				return result
			}
		}
		result, err := CheckFileExists(run.ProjectDir, rule)
		if err != nil {
			result.Status = OperationalError
			result.Reason = err.Error()
		}
		results[id] = result
		return result
	}
	for _, rule := range published.Contract.Rules {
		result := evaluate(rule.ID)
		attempt.Results = append(attempt.Results, result)
		if result.Status == OperationalError {
			attempt.Status = OperationalError
		} else if result.Status != Passed && attempt.Status != OperationalError {
			attempt.Status = Failed
		}
	}
	attempt.CompletedAt = time.Now().UTC()
	return store.AppendAttempt(run.ID, attempt)
}
