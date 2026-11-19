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
	// Blobs holds evidence content keyed by Evidence.Blob, for the
	// repository to store with the attempt. Reads leave it empty.
	Blobs map[string][]byte `json:"-"`
}
type Run struct {
	ID         string    `json:"id"`
	ContractID string    `json:"contractId"`
	Version    int       `json:"version"`
	ProjectDir string    `json:"projectDir"`
	Baseline   string    `json:"baseline"` // git commit at the start of the run, if any
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"createdAt"`
	Attempts   []Attempt `json:"attempts"`
}
type RunRepository interface {
	Repository
	CreateRun(Run) error
	GetRun(string) (Run, error)
	ListRuns() ([]Run, error)
	// AppendAttempt atomically numbers an attempt, saves results and evidence
	// blobs, and updates the run.
	AppendAttempt(string, Attempt) (Run, error)
	GetBlob(string) ([]byte, error)
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
	run := Run{ID: rand.Text(), ContractID: contractID, Version: version, ProjectDir: path, Baseline: Baseline(path), Status: "ready", CreatedAt: time.Now().UTC(), Attempts: []Attempt{}}
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

// GetEvidence returns stored evidence content by its Evidence.Blob key.
func (s *Service) GetEvidence(blob string) ([]byte, error) {
	store, err := s.runRepository()
	if err != nil {
		return nil, err
	}
	return store.GetBlob(blob)
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
	evidence := newCollector(run.ProjectDir, run.Baseline, s.LoadConfig)
	defer evidence.cleanup()
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
		var result RuleResult
		switch rule.Kind {
		case CommandSucceeds:
			item := evidence.command(rule.Command)
			result = RuleResult{RuleID: rule.ID, Evidence: []Evidence{item}}
			result.Status, result.Reason = commandVerdict(rule, item)
			result.Passed = result.Status == Passed
		default:
			var err error
			result, err = CheckFileExists(run.ProjectDir, rule)
			if err != nil {
				result.Status = OperationalError
				result.Reason = err.Error()
			}
		}
		// Evidence is collected with the check, from the same project state.
		// A rule can't pass if any of its evidence couldn't be collected.
		for _, spec := range rule.Evidence {
			if rule.Kind == CommandSucceeds && spec.Kind == CommandEvidence && spec.Command == rule.Command {
				continue // already recorded as the check itself
			}
			item := evidence.collect(spec)
			result.Evidence = append(result.Evidence, item)
			if item.Error != "" && result.Status == Passed {
				result.Status, result.Passed = OperationalError, false
				result.Reason = fmt.Sprintf("%s evidence couldn't be collected: %s", item.Kind, item.Error)
			}
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
	attempt.Blobs = evidence.blobs
	return store.AppendAttempt(run.ID, attempt)
}
