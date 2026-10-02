//go:build uitest

// Test-only transport for browser acceptance checks. It is not part of the app.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"harness/backend"
	"harness/storage"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"syscall"
	"time"
)

func main() {
	directory, err := os.MkdirTemp("", "harness-ui-db-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(directory)
	store, err := storage.Open(filepath.Join(directory, "harness.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	service := reflect.ValueOf(backend.NewService(store))
	allowed := map[string]bool{"SaveDraft": true, "GetDraft": true, "ListContracts": true, "PublishContract": true, "GetVersion": true, "ListVersions": true, "StartRun": true, "EvaluateAttempt": true, "GetRun": true, "ListRuns": true}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "http://127.0.0.1:5173")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var request struct {
			Method string            `json:"method"`
			Args   []json.RawMessage `json:"args"`
		}
		respond := func(value any, err error) {
			w.Header().Set("Content-Type", "application/json")
			response := map[string]any{"result": value}
			if err != nil {
				response["error"] = err.Error()
			}
			json.NewEncoder(w).Encode(response)
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil {
			respond(nil, err)
			return
		}
		if !allowed[request.Method] {
			respond(nil, fmt.Errorf("unknown test method"))
			return
		}
		method := service.MethodByName(request.Method)
		if len(request.Args) != method.Type().NumIn() {
			respond(nil, fmt.Errorf("wrong argument count"))
			return
		}
		inputs := []reflect.Value{}
		for i, arg := range request.Args {
			value := reflect.New(method.Type().In(i))
			if err := json.Unmarshal(arg, value.Interface()); err != nil {
				respond(nil, err)
				return
			}
			inputs = append(inputs, value.Elem())
		}
		outputs := method.Call(inputs)
		if !outputs[1].IsNil() {
			respond(nil, outputs[1].Interface().(error))
			return
		}
		respond(outputs[0].Interface(), nil)
	})
	server := &http.Server{Addr: "127.0.0.1:5174", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
