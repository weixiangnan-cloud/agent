package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pb "github.com/nezhahq/agent/proto"
)

func TestParseEmbyTarget(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantURL string
		wantErr bool
	}{
		{
			name:    "scheme alias",
			raw:     "emby://demo.example.com:443",
			wantURL: "https://demo.example.com:443",
		},
		{
			name:    "json config",
			raw:     `{"type":"emby","url":"https://demo.example.com","expected_name":"Demo"}`,
			wantURL: "https://demo.example.com",
		},
		{
			name:    "not emby",
			raw:     "https://demo.example.com",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := parseEmbyTarget(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseEmbyTarget() error = %v", err)
			}
			if cfg.URL != tt.wantURL {
				t.Fatalf("cfg.URL = %q, want %q", cfg.URL, tt.wantURL)
			}
		})
	}
}

func TestHandleEmbyProbeTaskFallsBackToEmbyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/System/Info/Public":
			http.NotFound(w, r)
		case "/emby/System/Info/Public":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ServerName":"DemoEmby","Version":"4.10.0.6","Id":"abc123"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := &pb.TaskResult{}
	task := &pb.Task{Data: "emby+http://" + server.Listener.Addr().String()}
	cfg, err := parseEmbyTarget(task.GetData())
	if err != nil {
		t.Fatalf("parseEmbyTarget() error = %v", err)
	}

	handleEmbyProbeTask(task, result, cfg)
	if !result.Successful {
		t.Fatalf("expected successful probe, got error: %s", result.Data)
	}
	if got, want := result.Data, "Emby OK: name=DemoEmby, version=4.10.0.6, id=abc123"; got != want {
		t.Fatalf("result.Data = %q, want %q", got, want)
	}
}

func TestHandleEmbyProbeTaskChecksExpectedFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ServerName":"DemoEmby","Version":"4.10.0.6","Id":"abc123"}`))
	}))
	defer server.Close()

	result := &pb.TaskResult{}
	cfg := &embyTargetConfig{
		Type:            "emby",
		URL:             "http://" + server.Listener.Addr().String(),
		ExpectedVersion: "4.10.1.0",
	}
	handleEmbyProbeTask(&pb.Task{Data: cfg.URL}, result, cfg)
	if result.Successful {
		t.Fatalf("expected failed probe due to version mismatch")
	}
	if result.Data == "" {
		t.Fatalf("expected mismatch message")
	}
}

func TestHandleEmbyProbeTaskIncludesCandidateFailures(t *testing.T) {
	result := &pb.TaskResult{}
	cfg := &embyTargetConfig{
		Type: "emby",
		URL:  "http://127.0.0.1:1",
	}

	handleEmbyProbeTask(&pb.Task{Data: cfg.URL}, result, cfg)
	if result.Successful {
		t.Fatalf("expected failed probe")
	}
	if result.Data == "" {
		t.Fatalf("expected detailed failure message")
	}
	if got := result.Data; !strings.Contains(got, "System/Info/Public") {
		t.Fatalf("expected candidate path in failure, got %q", got)
	}
}
