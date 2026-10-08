package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUploadLeavesLocalCopyWithDaemon(t *testing.T) {
	type copyReq struct {
		id, filename, auth, body string
	}
	var copies []copyReq
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		copies = append(copies, copyReq{
			id:       r.URL.Query().Get("attachment_id"),
			filename: r.URL.Query().Get("filename"),
			auth:     r.Header.Get("Authorization"),
			body:     string(body),
		})
		w.WriteHeader(http.StatusNotFound) // an older daemon: must not fail the upload
	}))
	defer daemon.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "att-1", "url": "https://cdn/x"})
	}))
	defer srv.Close()

	client := NewAPIClient(srv.URL, "ws", "mat_token")
	client.LocalCopyURL = daemon.URL + "/outputs"
	if _, err := client.UploadChatAttachment(context.Background(), []byte("hello"), "/work/out/report.html", "task-1"); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if len(copies) != 1 {
		t.Fatalf("copies = %d, want 1", len(copies))
	}
	got := copies[0]
	if got.id != "att-1" || got.filename != "report.html" || got.auth != "Bearer mat_token" || got.body != "hello" {
		t.Fatalf("copy request = %+v", got)
	}

	client.LocalCopyURL = ""
	if _, err := client.UploadIssueAttachment(context.Background(), []byte("hello"), "a.txt", ""); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if len(copies) != 1 {
		t.Fatalf("copy kept outside a daemon task")
	}
}

func TestUploadSucceedsWhenDaemonIsGone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "att-1", "url": "https://cdn/x"})
	}))
	defer srv.Close()
	client := NewAPIClient(srv.URL, "ws", "mat_token")
	client.LocalCopyURL = "http://127.0.0.1:1/outputs"
	if _, err := client.UploadIssueAttachment(context.Background(), []byte("hello"), "a.txt", ""); err != nil {
		t.Fatalf("upload failed because the daemon was unreachable: %v", err)
	}
}
