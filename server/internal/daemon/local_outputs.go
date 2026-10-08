package daemon

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon/localoutputs"
)

// localOutputsDirName is the copy library under the profile dir.
const localOutputsDirName = "outputs"

// maxLocalOutputBytes bounds one copy. Uploads past the server's own limit
// never get an attachment id, so this only guards the daemon from a runaway
// request body.
const maxLocalOutputBytes = 2 << 30

func newLocalOutputsStore(profile string) *localoutputs.Store {
	dir, err := cli.ProfileDir(profile)
	if err != nil {
		return nil
	}
	return &localoutputs.Store{Root: filepath.Join(dir, localOutputsDirName)}
}

// localOutputsHandler serves the copy library on the 127.0.0.1 listener.
//
//	POST /outputs?attachment_id=<id>&filename=<name>  body = file bytes
//	     Only an agent task this daemon is running may write (task token).
//	GET  /outputs?attachment_id=<id>
//	     200 {attachment_id, filename, size_bytes, path} while the copy still
//	     matches what was written; 404 otherwise. The desktop main process is
//	     the reader and the path never leaves this machine.
func (d *Daemon) localOutputsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.localOutputs == nil {
			http.Error(w, "local outputs unavailable", http.StatusServiceUnavailable)
			return
		}
		id := strings.TrimSpace(r.URL.Query().Get("attachment_id"))
		if !localoutputs.ValidID(id) {
			http.Error(w, "attachment_id must be an attachment id", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodGet:
			entry, err := d.localOutputs.Lookup(id)
			if errors.Is(err, localoutputs.ErrNotFound) {
				http.Error(w, "no local copy", http.StatusNotFound)
				return
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeDaemonJSON(w, http.StatusOK, localOutputResponse(entry))
		case http.MethodPost:
			if _, authResult := d.activeRepoCheckoutTask(r); authResult != repoCheckoutAuthOK {
				http.Error(w, "only a running agent task may keep a local copy", http.StatusUnauthorized)
				return
			}
			body := http.MaxBytesReader(w, r.Body, maxLocalOutputBytes)
			entry, err := d.localOutputs.Put(id, r.URL.Query().Get("filename"), body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeDaemonJSON(w, http.StatusOK, localOutputResponse(entry))
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func localOutputResponse(e localoutputs.Entry) map[string]any {
	return map[string]any{
		"attachment_id": e.AttachmentID,
		"filename":      e.Filename,
		"size_bytes":    e.SizeBytes,
		"path":          e.Path,
	}
}
