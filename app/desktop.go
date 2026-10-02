package main

import (
	"context"
	"fmt"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"harness/backend"
	"harness/storage"
)

// Desktop is a thin Wails boundary. All acceptance logic lives in backend.Service.
type Desktop struct {
	ctx     context.Context
	service *backend.Service
	store   *storage.SQLite
}

func NewDesktop(databasePath string) (*Desktop, error) {
	store, err := storage.Open(databasePath)
	if err != nil {
		return nil, err
	}
	return &Desktop{store: store, service: backend.NewService(store)}, nil
}
func (d *Desktop) startup(ctx context.Context) { d.ctx = ctx }
func (d *Desktop) shutdown(context.Context)    { d.store.Close() }
func (d *Desktop) SelectProject() (string, error) {
	if d.ctx == nil {
		return "", fmt.Errorf("project selection requires the desktop window")
	}
	return runtime.OpenDirectoryDialog(d.ctx, runtime.OpenDialogOptions{Title: "Select project to check"})
}
func (d *Desktop) SaveDraft(c backend.Contract) (backend.Contract, error) {
	return d.service.SaveDraft(c)
}
func (d *Desktop) GetDraft(id string) (backend.Contract, error) { return d.service.GetDraft(id) }
func (d *Desktop) ListContracts() ([]backend.Contract, error)   { return d.service.ListContracts() }
func (d *Desktop) PublishContract(id string) (backend.PublishedContract, error) {
	return d.service.PublishContract(id)
}
func (d *Desktop) GetVersion(id string, v int) (backend.PublishedContract, error) {
	return d.service.GetVersion(id, v)
}
func (d *Desktop) ListVersions(id string) ([]backend.PublishedContract, error) {
	return d.service.ListVersions(id)
}
func (d *Desktop) StartRun(id string, v int, path string) (backend.Run, error) {
	return d.service.StartRun(id, v, path)
}
func (d *Desktop) EvaluateAttempt(id string) (backend.Run, error) {
	return d.service.EvaluateAttempt(id)
}
func (d *Desktop) GetRun(id string) (backend.Run, error) { return d.service.GetRun(id) }
func (d *Desktop) ListRuns() ([]backend.Run, error)      { return d.service.ListRuns() }
