package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/storage"
)

// TestUploadDownload_NonASCIIFilenames round-trips Chinese, emoji and
// space-bearing filenames through the real local-disk backend (DENE-1089):
// the agent chat-reply upload path (`multica attachment upload`), the plain
// workspace upload path (Web / `--attachment`), and the proxied download. The
// stored row and the download's Content-Disposition must both give back the
// exact original name.
func TestUploadDownload_NonASCIIFilenames(t *testing.T) {
	if testPool == nil {
		t.Skip("test database not available")
	}
	t.Setenv("LOCAL_UPLOAD_DIR", t.TempDir())
	t.Setenv("LOCAL_UPLOAD_BASE_URL", "")
	local := storage.NewLocalStorageFromEnv()
	if local == nil {
		t.Fatal("NewLocalStorageFromEnv returned nil")
	}
	origStorage := testHandler.Storage
	origCfg := testHandler.cfg
	origSigner := testHandler.CFSigner
	testHandler.Storage = local
	testHandler.cfg.AttachmentDownloadMode = "proxy"
	testHandler.CFSigner = nil
	t.Cleanup(func() {
		testHandler.Storage = origStorage
		testHandler.cfg = origCfg
		testHandler.CFSigner = origSigner
	})

	agentID := createHandlerTestAgent(t, "UnicodeFilenameAgent", []byte("[]"))
	sessionID := createHandlerTestChatSession(t, agentID)
	taskID := seedRunningChatTask(t, agentID, sessionID)

	names := []string{"跨线程调研.md", "报告 😀 final.md", "my report v2.md"}
	paths := map[string]map[string]string{
		"chat task": {
			"X-User-ID":      testUserID,
			"X-Workspace-ID": testWorkspaceID,
			"X-Actor-Source": "task_token",
			"X-Agent-ID":     agentID,
			"X-Task-ID":      taskID,
		},
		"workspace": {
			"X-User-ID":      testUserID,
			"X-Workspace-ID": testWorkspaceID,
		},
	}

	for path, headers := range paths {
		for _, name := range names {
			t.Run(path+"/"+name, func(t *testing.T) {
				content := []byte("# " + name + "\n")
				var body bytes.Buffer
				mw := multipart.NewWriter(&body)
				part, err := mw.CreateFormFile("file", name)
				if err != nil {
					t.Fatal(err)
				}
				part.Write(content)
				if headers["X-Task-ID"] != "" {
					if err := mw.WriteField("task_id", headers["X-Task-ID"]); err != nil {
						t.Fatal(err)
					}
				}
				mw.Close()

				req := httptest.NewRequest(http.MethodPost, "/api/upload-file", &body)
				req.Header.Set("Content-Type", mw.FormDataContentType())
				for k, v := range headers {
					req.Header.Set(k, v)
				}
				w := httptest.NewRecorder()
				testHandler.UploadFile(w, req)
				if w.Code != http.StatusOK {
					t.Fatalf("upload: status = %d, body = %s", w.Code, w.Body.String())
				}
				var resp AttachmentResponse
				if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
					t.Fatalf("decode upload response: %v; body = %s", err, w.Body.String())
				}
				t.Cleanup(func() {
					testPool.Exec(context.Background(), `DELETE FROM attachment WHERE id = $1`, resp.ID)
				})
				if resp.Filename != name {
					t.Fatalf("stored filename = %q, want %q", resp.Filename, name)
				}

				dreq, dw := newDownloadRequest(t, resp.ID, testWorkspaceID)
				testHandler.DownloadAttachment(dw, dreq)
				if dw.Code != http.StatusOK {
					t.Fatalf("download: status = %d, body = %s", dw.Code, dw.Body.String())
				}
				if got := dw.Body.String(); got != string(content) {
					t.Fatalf("download body = %q, want %q", got, content)
				}
				disposition := dw.Header().Get("Content-Disposition")
				_, params, err := mime.ParseMediaType(disposition)
				if err != nil {
					t.Fatalf("parse Content-Disposition %q: %v", disposition, err)
				}
				if params["filename"] != name {
					t.Fatalf("download filename = %q (header %q), want %q", params["filename"], disposition, name)
				}
			})
		}
	}
}
