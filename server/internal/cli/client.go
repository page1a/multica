package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ClientVersion is the CLI version sent on every request as X-Client-Version.
// Set by the multica binary at init() so the package doesn't depend on the
// concrete cmd package. Defaults to "dev" when running unset (e.g. tests).
var ClientVersion = "dev"

// ClientPlatform identifies this client to the server. Override for tests
// or alternative entry points; defaults to "cli".
var ClientPlatform = "cli"

// ClientOS is the normalized operating system string sent as X-Client-OS.
// Computed once from runtime.GOOS so the server doesn't need to reverse-map
// Go's os names ("darwin"/"windows"/"linux") into the protocol vocabulary.
var ClientOS = normalizeGOOS(runtime.GOOS)

func normalizeGOOS(goos string) string {
	switch goos {
	case "darwin":
		return "macos"
	case "windows":
		return "windows"
	case "linux":
		return "linux"
	default:
		return goos
	}
}

// APIClient is a REST client for the Multica server API.
// Used by ctrl subcommands (agent, runtime, status, etc.). Requests
// automatically include auth and execution context headers when configured.
type APIClient struct {
	BaseURL     string
	WorkspaceID string
	Token       string
	// AgentID / TaskID travel as X-Agent-ID / X-Task-ID execution context.
	// They are NOT what makes the server treat a request as the agent's: the
	// server strips both headers on entry and re-stamps them from the bound
	// mat_ task token, so attribution follows the token, not these fields
	// (MUL-3428). The CLI only sets them inside a daemon-managed task, where
	// MULTICA_TOKEN is that mat_ token.
	AgentID    string
	TaskID     string
	HTTPClient *http.Client

	// Identity overrides. Empty values fall back to the package-level
	// ClientPlatform / ClientVersion / ClientOS.
	Platform string
	Version  string
	OS       string
}

// TaskTokenPrefix marks a task-scoped agent token, as minted by
// internal/auth.NewAgentTaskToken. A 401 on one is not the "your login
// expired" that a 401 on a member credential is: the token belongs to a single
// run and there is no sign-in that brings it back. Why it was rejected is not
// something the CLI can see — a terminal task is the usual cause, but a
// malformed token, one sent to the wrong server, and one dropped by an
// unrelated cleanup all look identical from here.
const TaskTokenPrefix = "mat_"

type HTTPError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
	// TaskScoped records that the failing request actually carried a
	// task-scoped `mat_` token. It changes nothing about the request; it only
	// lets FormatError tell a 401 worth signing in again for from one where
	// the right move is to stop (GH #7522).
	TaskScoped bool
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s %s returned %d: %s", e.Method, e.Path, e.StatusCode, strings.TrimSpace(e.Body))
}

const (
	// httpErrorBodyLimit caps how much of an error response the generic paths
	// keep. Enough for {"error": "..."} plus context, small enough that a
	// misbehaving endpoint cannot balloon CLI memory.
	httpErrorBodyLimit = 4096
	// transferErrorBodyLimit applies to /transfer/* failures. Those responses
	// put the whole import report (one item per entity, so tens of thousands of
	// bytes for a real workspace) next to "error"/"code" in the same JSON
	// object. Truncating it mid-object left the CLI with unparseable JSON and
	// nothing to show the user (DENE-318), so transfer errors keep the full
	// structured body.
	transferErrorBodyLimit = 1 << 20
)

type resumableUploadState struct {
	UploadID string    `json:"upload_id"`
	Updated  time.Time `json:"updated_at"`
}

func resumableUploadStatePath() string {
	if p := strings.TrimSpace(os.Getenv("MULTICA_UPLOAD_STATE_FILE")); p != "" {
		return p
	}
	cache, err := os.UserCacheDir()
	if err != nil || cache == "" {
		return ""
	}
	return filepath.Join(cache, "multica", "resumable-uploads.json")
}

func resumableUploadKey(baseURL, filename string, size int, fields map[string]string) string {
	b, _ := json.Marshal(struct {
		BaseURL string            `json:"base_url"`
		Name    string            `json:"name"`
		Size    int               `json:"size"`
		Fields  map[string]string `json:"fields"`
	}{baseURL, filepath.Base(filename), size, fields})
	return string(b)
}

func loadResumableUpload(key string) string {
	p := resumableUploadStatePath()
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	var states map[string]resumableUploadState
	if json.Unmarshal(b, &states) != nil {
		return ""
	}
	s, ok := states[key]
	if !ok || s.UploadID == "" || time.Since(s.Updated) > 24*time.Hour {
		return ""
	}
	return s.UploadID
}

func saveResumableUpload(key, uploadID string) {
	p := resumableUploadStatePath()
	if p == "" || uploadID == "" {
		return
	}
	states := map[string]resumableUploadState{}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &states)
	}
	if states == nil {
		states = map[string]resumableUploadState{}
	}
	states[key] = resumableUploadState{UploadID: uploadID, Updated: time.Now()}
	b, err := json.Marshal(states)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(p, b, 0o600)
}

func clearResumableUpload(key string) {
	p := resumableUploadStatePath()
	if p == "" {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var states map[string]resumableUploadState
	if json.Unmarshal(b, &states) != nil {
		return
	}
	delete(states, key)
	if len(states) == 0 {
		_ = os.Remove(p)
		return
	}
	if out, err := json.Marshal(states); err == nil {
		_ = os.WriteFile(p, out, 0o600)
	}
}

// errorBodyLimit picks the read cap for a failing request path.
func errorBodyLimit(path string) int64 {
	if strings.Contains(path, "/transfer/") {
		return transferErrorBodyLimit
	}
	return httpErrorBodyLimit
}

// newHTTPError builds a *HTTPError from an error response (status >= 400),
// reading a capped slice of the body. Every Multica API helper funnels its
// >= 400 responses through this so the top-level FormatError / ExitCodeFor can
// classify the failure via errors.As(err, **HTTPError) regardless of which
// HTTP verb the command used.
func newHTTPError(method, path string, resp *http.Response) *HTTPError {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit(path)))
	return &HTTPError{
		Method:     method,
		Path:       path,
		StatusCode: resp.StatusCode,
		Body:       strings.TrimSpace(string(data)),
		TaskScoped: requestUsedTaskToken(resp),
	}
}

// requestUsedTaskToken reports whether the request that produced resp actually
// sent a task token.
//
// Reading the client's own Token field would be wrong twice over. DownloadFile
// deliberately sends no Authorization header for an absolute signed URL, so a
// 401 from object storage would be reported as a rejected task token purely
// because the client happened to hold a `mat_` token. And Go strips Authorization
// across a cross-host redirect, so the request that was sent is not always the
// one the caller built. resp.Request is the request that actually went out,
// after redirects, which is the only thing this claim can honestly rest on.
func requestUsedTaskToken(resp *http.Response) bool {
	if resp == nil || resp.Request == nil {
		return false
	}
	return strings.HasPrefix(resp.Request.Header.Get("Authorization"), "Bearer "+TaskTokenPrefix)
}

// defaultHTTPTimeout is the per-request timeout for the CLI's HTTP client.
// It can be overridden with the MULTICA_HTTP_TIMEOUT environment variable
// (see httpTimeout). 30s is chosen over the historical 15s because complex
// networks (notably in mainland China) routinely need more than 15s to
// complete the TLS handshake plus request round-trip, which surfaced as an
// opaque "context deadline exceeded" to users.
const defaultHTTPTimeout = 30 * time.Second

// httpTimeout returns the HTTP client timeout, honoring MULTICA_HTTP_TIMEOUT.
// The value may be a Go duration string ("45s", "2m") or a plain integer
// number of seconds ("45"). Invalid or non-positive values fall back to the
// default.
func httpTimeout() time.Duration {
	v := strings.TrimSpace(os.Getenv("MULTICA_HTTP_TIMEOUT"))
	if v == "" {
		return defaultHTTPTimeout
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return defaultHTTPTimeout
}

// apiContextGrace is added on top of the HTTP transport timeout when deriving
// a command-level context deadline, so the transport timeout (which produces a
// clean, classifiable "request timed out" error) is the one that fires rather
// than the outer context being canceled first.
const apiContextGrace = 5 * time.Second

// APITimeout returns the deadline budget for a single CLI API command. It is
// always at least the configured HTTP transport timeout (see httpTimeout,
// which honors MULTICA_HTTP_TIMEOUT) plus a small grace margin, so a
// command-level context never truncates an in-flight request below the timeout
// the user configured. This is the fix for command contexts that previously
// hardcoded a 15s deadline shorter than the 30s/env transport timeout.
func APITimeout() time.Duration {
	return AtLeastAPITimeout(0)
}

// AtLeastAPITimeout returns max(min, APITimeout()). Use it for commands that
// need a larger floor than usual (for example file uploads, which historically
// used a 60s budget).
func AtLeastAPITimeout(min time.Duration) time.Duration {
	budget := httpTimeout() + apiContextGrace
	if min > budget {
		return min
	}
	return budget
}

// APIContext derives a command-scoped context whose deadline is APITimeout().
// The returned cancel func must be called (typically via defer) to release
// resources. Commands should use this instead of context.WithTimeout with a
// hardcoded duration so the deadline always respects MULTICA_HTTP_TIMEOUT.
func APIContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, APITimeout())
}

// NewAPIClient creates a new API client for ctrl commands.
func NewAPIClient(baseURL, workspaceID, token string) *APIClient {
	return &APIClient{
		BaseURL:     strings.TrimRight(baseURL, "/"),
		WorkspaceID: workspaceID,
		Token:       token,
		HTTPClient:  &http.Client{Timeout: httpTimeout()},
	}
}

// clientCapabilities is the X-Client-Capabilities value every CLI request
// advertises. It is a protocol detail, not a user-visible flag: the CLI always
// knows how to handle these response shapes, so there is nothing for a caller
// to opt into.
//
// stable_attachment_urls asks bulk responses to return the stable
// /api/attachments/{id}/download path instead of a ~800-char CloudFront
// signature that is re-minted on every request (MUL-5372 / GitHub #5999). The
// CLI never hands an attachment URL to a native loader — `multica attachment
// download <id>` fetches a fresh signature from the single-attachment endpoint,
// which keeps signing regardless of this capability — so the signature in list
// payloads was pure cost: raw bytes, a per-attachment RSA sign, and bytes that
// differ on every read and therefore defeat agent prompt caching.
const clientCapabilities = "stable_attachment_urls"

func (c *APIClient) setHeaders(req *http.Request) {
	req.Header.Set("X-Client-Capabilities", clientCapabilities)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.WorkspaceID != "" {
		req.Header.Set("X-Workspace-ID", c.WorkspaceID)
	}
	if c.AgentID != "" {
		req.Header.Set("X-Agent-ID", c.AgentID)
	}
	if c.TaskID != "" {
		req.Header.Set("X-Task-ID", c.TaskID)
	}

	platform := c.Platform
	if platform == "" {
		platform = ClientPlatform
	}
	if platform != "" {
		req.Header.Set("X-Client-Platform", platform)
	}
	version := c.Version
	if version == "" {
		version = ClientVersion
	}
	if version != "" {
		req.Header.Set("X-Client-Version", version)
	}
	osName := c.OS
	if osName == "" {
		osName = ClientOS
	}
	if osName != "" {
		req.Header.Set("X-Client-OS", osName)
	}
}

// GetJSON performs a GET request and decodes the JSON response.
//
// On an HTTP error response (status >= 400) the returned error is a
// *HTTPError so callers can use errors.As to inspect the status code
// (for example to recognize a 404 from a server that does not expose a
// given endpoint and degrade gracefully). The error string format
// ("GET <path> returned <code>: <body>") is preserved by HTTPError.Error().
func (c *APIClient) GetJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	c.setHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return newHTTPError(http.MethodGet, path, resp)
	}
	if out == nil {
		return nil
	}
	return wrapBodyRead(req, json.NewDecoder(resp.Body).Decode(out))
}

// GetJSONWithHeaders performs a GET request, decodes the JSON response, and
// returns the response headers. Useful when callers need header values like
// X-Total-Count for pagination.
func (c *APIClient) GetJSONWithHeaders(ctx context.Context, path string, out any) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, newHTTPError(http.MethodGet, path, resp)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.Header, wrapBodyRead(req, err)
		}
	}
	return resp.Header, nil
}

// GetBytes performs a GET request and returns the raw response body plus the
// response headers. `logs export` needs this rather than GetJSON: it writes the
// server-rendered artifact byte-for-byte, and decoding then re-encoding would
// change formatting and map key order, breaking "the CLI and the dialog produce
// the same artifact".
func (c *APIClient) GetBytes(ctx context.Context, path string) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, nil, err
	}
	c.setHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, resp.Header, newHTTPError(http.MethodGet, path, resp)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.Header, wrapBodyRead(req, err)
	}
	return body, resp.Header, nil
}

// DeleteJSON performs a DELETE request.
func (c *APIClient) DeleteJSON(ctx context.Context, path string) error {
	return c.DeleteJSONResponse(ctx, path, nil)
}

// DeleteJSONResponse performs a DELETE request and optionally decodes the JSON response.
func (c *APIClient) DeleteJSONResponse(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	c.setHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return newHTTPError(http.MethodDelete, path, resp)
	}
	if out != nil {
		return wrapBodyRead(req, json.NewDecoder(resp.Body).Decode(out))
	}
	return nil
}

// DeleteJSONWithBody performs a DELETE request with a JSON body.
func (c *APIClient) DeleteJSONWithBody(ctx context.Context, path string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return newHTTPError(http.MethodDelete, path, resp)
	}
	return nil
}

// PostEncoded performs a POST with a body the caller has already built and
// optionally encoded.
//
// PostJSON marshals for you, which is exactly what the transfer sender cannot
// use: it compresses the body, measures the compressed bytes, and has to
// re-slice a body the edge dropped, so the bytes on the wire are its own. What
// it still needs from this client is the transport, the auth/identity headers,
// the size-capped error body, and the *HTTPError / *NetworkError classification
// every caller of this package already branches on. contentEncoding, when set,
// is sent verbatim as Content-Encoding and tells the server how the body was
// encoded; the caller is responsible for the server understanding it (the
// /transfer/* endpoints advertise that through /health).
func (c *APIClient) PostEncoded(ctx context.Context, path, contentType, contentEncoding string, payload []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	if contentEncoding != "" {
		req.Header.Set("Content-Encoding", contentEncoding)
	}
	c.setHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return newHTTPError(http.MethodPost, path, resp)
	}
	if out == nil {
		return nil
	}
	return wrapBodyRead(req, json.NewDecoder(resp.Body).Decode(out))
}

// PostJSON performs a POST request with a JSON body.
func (c *APIClient) PostJSON(ctx context.Context, path string, body any, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return newHTTPError(http.MethodPost, path, resp)
	}
	if out == nil {
		return nil
	}
	return wrapBodyRead(req, json.NewDecoder(resp.Body).Decode(out))
}

// PutJSON performs a PUT request with a JSON body.
func (c *APIClient) PutJSON(ctx context.Context, path string, body any, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return newHTTPError(http.MethodPut, path, resp)
	}
	if out == nil {
		return nil
	}
	return wrapBodyRead(req, json.NewDecoder(resp.Body).Decode(out))
}

// PatchJSON performs a PATCH request with a JSON body.
func (c *APIClient) PatchJSON(ctx context.Context, path string, body any, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return newHTTPError(http.MethodPatch, path, resp)
	}
	if out == nil {
		return nil
	}
	return wrapBodyRead(req, json.NewDecoder(resp.Body).Decode(out))
}

// AttachmentResponse mirrors the server's upload-file response.
type AttachmentResponse struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	DownloadURL string `json:"download_url"`
	// MarkdownURL is the durable, persistable URL to embed in markdown bodies
	// (chat replies, comments). Unlike DownloadURL it never carries a TTL.
	MarkdownURL string `json:"markdown_url"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	CreatedAt   string `json:"created_at"`
}

func (c *APIClient) uploadChunked(ctx context.Context, data []byte, filename string, fields map[string]string) (AttachmentResponse, error) {
	if len(data) <= 2<<20 {
		return AttachmentResponse{}, fmt.Errorf("chunked upload requires a large payload")
	}
	stateKey := resumableUploadKey(c.BaseURL, filename, len(data), fields)
	uploadID := loadResumableUpload(stateKey)
	httpClient := c.HTTPClient
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > httpClient.Timeout {
			copyClient := *httpClient
			copyClient.Timeout = remaining
			httpClient = &copyClient
		}
	}
	meta := map[string]any{"filename": filepath.Base(filename), "size": len(data)}
	for k, v := range fields {
		if v != "" {
			meta[k] = v
		}
	}
	b, _ := json.Marshal(meta)
	var s struct {
		UploadID  string `json:"upload_id"`
		ChunkSize int    `json:"chunk_size"`
	}
	if uploadID != "" {
		statusReq, e := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/upload-file/chunked/"+url.PathEscape(uploadID), nil)
		if e == nil {
			c.setHeaders(statusReq)
			statusResp, doErr := httpClient.Do(statusReq)
			if doErr == nil && statusResp.StatusCode < 400 {
				var status struct {
					UploadID string `json:"upload_id"`
					Size     int    `json:"size"`
				}
				_ = json.NewDecoder(statusResp.Body).Decode(&status)
				statusResp.Body.Close()
				if status.UploadID == uploadID && status.Size == len(data) {
					s.UploadID = uploadID
					s.ChunkSize = 2 << 20
				} else {
					uploadID = ""
				}
			} else {
				if statusResp != nil {
					statusResp.Body.Close()
				}
				uploadID = ""
			}
		}
	}
	if uploadID == "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/upload-file/chunked", bytes.NewReader(b))
		if err != nil {
			return AttachmentResponse{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		c.setHeaders(req)
		resp, err := httpClient.Do(req)
		if err != nil {
			return AttachmentResponse{}, err
		}
		if resp.StatusCode >= 400 {
			defer resp.Body.Close()
			return AttachmentResponse{}, newHTTPError(http.MethodPost, "/api/upload-file/chunked", resp)
		}
		if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
			resp.Body.Close()
			return AttachmentResponse{}, err
		}
		resp.Body.Close()
		uploadID = s.UploadID
		if uploadID == "" {
			return AttachmentResponse{}, fmt.Errorf("missing upload id")
		}
		saveResumableUpload(stateKey, uploadID)
	}
	if s.ChunkSize <= 0 {
		s.ChunkSize = 2 << 20
	}
	uploaded := map[int]bool{}
	statusReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/upload-file/chunked/"+url.PathEscape(uploadID), nil)
	if err == nil {
		c.setHeaders(statusReq)
		if statusResp, e := httpClient.Do(statusReq); e == nil {
			if statusResp.StatusCode < 400 {
				var status struct {
					Chunks []int `json:"chunks"`
				}
				_ = json.NewDecoder(statusResp.Body).Decode(&status)
				for _, idx := range status.Chunks {
					uploaded[idx] = true
				}
			}
			statusResp.Body.Close()
		}
	}
	for i, off := 0, 0; off < len(data); i, off = i+1, off+s.ChunkSize {
		end := off + s.ChunkSize
		if end > len(data) {
			end = len(data)
		}
		if uploaded[i] {
			continue
		}
		path := fmt.Sprintf("%s/api/upload-file/chunked/%s/chunk?index=%d", c.BaseURL, uploadID, i)
		var last error
		for attempt := 0; attempt < 3; attempt++ {
			rq, e := http.NewRequestWithContext(ctx, http.MethodPut, path, bytes.NewReader(data[off:end]))
			if e != nil {
				last = e
				continue
			}
			c.setHeaders(rq)
			rr, e := httpClient.Do(rq)
			if e == nil && rr.StatusCode < 400 {
				rr.Body.Close()
				last = nil
				break
			}
			if e != nil {
				last = e
			} else {
				last = newHTTPError(http.MethodPut, "/api/upload-file/chunked", rr)
				rr.Body.Close()
			}
		}
		if last != nil {
			return AttachmentResponse{}, last
		}
	}
	fin, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/api/upload-file/chunked/%s/complete", c.BaseURL, uploadID), nil)
	if err != nil {
		return AttachmentResponse{}, err
	}
	c.setHeaders(fin)
	rr, err := httpClient.Do(fin)
	if err != nil {
		return AttachmentResponse{}, err
	}
	defer rr.Body.Close()
	if rr.StatusCode >= 400 {
		return AttachmentResponse{}, newHTTPError(http.MethodPost, "/api/upload-file/chunked/complete", rr)
	}
	var out AttachmentResponse
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		return AttachmentResponse{}, err
	}
	clearResumableUpload(stateKey)
	return out, nil
}

// UploadFile uploads a file via multipart form to /api/upload-file.
// It returns the attachment ID from the server response.
func (c *APIClient) UploadFile(ctx context.Context, fileData []byte, filename string, issueID string) (string, error) {
	att, err := c.UploadIssueAttachment(ctx, fileData, filename, issueID)
	if err != nil {
		return "", err
	}
	return att.ID, nil
}

// UploadIssueAttachment uploads a file via multipart form to /api/upload-file
// and returns the full AttachmentResponse. An empty issueID leaves the row
// unbound, which is what `issue create` needs: the file is uploaded before the
// issue exists, its `markdown_url` goes into the description, and the create
// call binds it via `attachment_ids`. Files over 2 MiB go through the
// resumable chunked upload instead.
func (c *APIClient) UploadIssueAttachment(ctx context.Context, fileData []byte, filename string, issueID string) (AttachmentResponse, error) {
	if len(fileData) > 2<<20 {
		fields := map[string]string{}
		if issueID != "" {
			fields["issue_id"] = issueID
		}
		return c.uploadChunked(ctx, fileData, filename, fields)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", filepath.Base(filename))
	if err != nil {
		return AttachmentResponse{}, fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(fileData); err != nil {
		return AttachmentResponse{}, fmt.Errorf("write file data: %w", err)
	}

	if issueID != "" {
		if err := writer.WriteField("issue_id", issueID); err != nil {
			return AttachmentResponse{}, fmt.Errorf("write issue_id field: %w", err)
		}
	}

	if err := writer.Close(); err != nil {
		return AttachmentResponse{}, fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/upload-file", &body)
	if err != nil {
		return AttachmentResponse{}, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c.setHeaders(req)

	// Honor a longer context deadline for large files: callers widen the
	// context for uploads, which the default client timeout would shadow.
	// Same shape as UploadChatAttachment / UploadFileWithURL.
	httpClient := c.HTTPClient
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > httpClient.Timeout {
			clientCopy := *httpClient
			clientCopy.Timeout = remaining
			httpClient = &clientCopy
		}
	}

	resp, err := httpClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return AttachmentResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return AttachmentResponse{}, newHTTPError(http.MethodPost, "/api/upload-file", resp)
	}

	var result AttachmentResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return AttachmentResponse{}, fmt.Errorf("decode upload response: %w", err)
	}
	if result.ID == "" {
		return AttachmentResponse{}, fmt.Errorf("upload response missing attachment id")
	}
	return result, nil
}

// UploadChatAttachment uploads a file via multipart form to /api/upload-file
// tagged with a chat task (task_id). The server binds the row to the assistant
// reply that task produces on completion. Returns the full AttachmentResponse
// (id + markdown_url) so the agent can embed the image inline in its reply.
func (c *APIClient) UploadChatAttachment(ctx context.Context, fileData []byte, filename, taskID string) (AttachmentResponse, error) {
	if len(fileData) > 2<<20 {
		return c.uploadChunked(ctx, fileData, filename, map[string]string{"task_id": taskID})
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", filepath.Base(filename))
	if err != nil {
		return AttachmentResponse{}, fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(fileData); err != nil {
		return AttachmentResponse{}, fmt.Errorf("write file data: %w", err)
	}
	if taskID != "" {
		if err := writer.WriteField("task_id", taskID); err != nil {
			return AttachmentResponse{}, fmt.Errorf("write task_id field: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return AttachmentResponse{}, fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/upload-file", &body)
	if err != nil {
		return AttachmentResponse{}, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c.setHeaders(req)

	// Honor a longer context deadline for large images, same as UploadFileWithURL.
	httpClient := c.HTTPClient
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > httpClient.Timeout {
			clientCopy := *httpClient
			clientCopy.Timeout = remaining
			httpClient = &clientCopy
		}
	}

	resp, err := httpClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return AttachmentResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return AttachmentResponse{}, newHTTPError(http.MethodPost, "/api/upload-file", resp)
	}

	var result AttachmentResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return AttachmentResponse{}, fmt.Errorf("decode upload response: %w", err)
	}
	if result.ID == "" {
		return AttachmentResponse{}, fmt.Errorf("upload response missing attachment id")
	}
	return result, nil
}

// UploadFileWithURL uploads a file via multipart form to /api/upload-file
// without associating it with an issue or comment. It decodes the full
// AttachmentResponse and returns the attachment ID and URL.
func (c *APIClient) UploadFileWithURL(ctx context.Context, fileData []byte, filename string) (string, string, error) {
	if len(fileData) > 2<<20 {
		out, err := c.uploadChunked(ctx, fileData, filename, nil)
		return out.ID, out.URL, err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", filepath.Base(filename))
	if err != nil {
		return "", "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(fileData); err != nil {
		return "", "", fmt.Errorf("write file data: %w", err)
	}

	if err := writer.Close(); err != nil {
		return "", "", fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/upload-file", &body)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c.setHeaders(req)

	// Use a client that respects the context deadline for slow uploads
	// (e.g. avatar uploads with 5MB files). The default HTTP client timeout
	// shadows any longer context deadline.
	httpClient := c.HTTPClient
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > httpClient.Timeout {
			clientCopy := *httpClient
			clientCopy.Timeout = remaining
			httpClient = &clientCopy
		}
	}

	resp, err := httpClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", "", newHTTPError(http.MethodPost, "/api/upload-file", resp)
	}

	var result AttachmentResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", fmt.Errorf("decode upload response: %w", err)
	}
	if result.URL == "" {
		return "", "", fmt.Errorf("upload response missing attachment url")
	}
	// Allow empty ID: the server returns id="" in the fallback path where
	// S3 upload succeeded but the attachment DB record failed. The file
	// is still usable via its URL.
	return result.ID, result.URL, nil
}

// ImportSkillFile imports a skill from a local archive (.skill / .zip) by
// POSTing it as multipart/form-data to /api/skills/import, alongside the
// on_conflict strategy. The structured import result is decoded into out.
func (c *APIClient) ImportSkillFile(ctx context.Context, fileData []byte, filename, onConflict string, out any) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", filepath.Base(filename))
	if err != nil {
		return fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(fileData); err != nil {
		return fmt.Errorf("write file data: %w", err)
	}
	if onConflict != "" {
		if err := writer.WriteField("on_conflict", onConflict); err != nil {
			return fmt.Errorf("write on_conflict field: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/skills/import", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c.setHeaders(req)

	// Respect a longer context deadline for slow uploads, mirroring
	// UploadFileWithURL: the default client timeout would otherwise shadow it.
	httpClient := c.HTTPClient
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > httpClient.Timeout {
			clientCopy := *httpClient
			clientCopy.Timeout = remaining
			httpClient = &clientCopy
		}
	}

	resp, err := httpClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return newHTTPError(http.MethodPost, "/api/skills/import", resp)
	}
	if out == nil {
		return nil
	}
	return wrapBodyRead(req, json.NewDecoder(resp.Body).Decode(out))
}

// UploadPrivatePlugin installs workspace-private Plugin archive bytes. The
// filename is transport metadata only; the Server derives source identity from
// validated manifest content and digests.
func (c *APIClient) UploadPrivatePlugin(ctx context.Context, path string, archive []byte, filename string, out any) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("artifact", filepath.Base(filename))
	if err != nil {
		return fmt.Errorf("create Plugin artifact form file: %w", err)
	}
	if _, err := part.Write(archive); err != nil {
		return fmt.Errorf("write Plugin artifact: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close Plugin artifact upload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c.setHeaders(req)
	resp, err := c.HTTPClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return newHTTPError(http.MethodPost, path, resp)
	}
	if out == nil {
		return nil
	}
	return wrapBodyRead(req, json.NewDecoder(resp.Body).Decode(out))
}

// DownloadFile downloads a file from the given URL and returns the response body.
// This is used for downloading attachments via their signed download_url.
// Downloads are limited to 100 MB to match the upload size limit.
//
// The URL may be absolute (a signed CloudFront/S3 URL) or relative
// (a server-relative path like "/api/attachments/{id}/download" or
// "/uploads/...") depending on how the
// server is configured. Relative URLs are resolved against the client's
// BaseURL and sent with the standard auth headers; absolute URLs are
// used as-is so that their query-string signatures are not disturbed.
func (c *APIClient) DownloadFile(ctx context.Context, downloadURL string) ([]byte, error) {
	isRelative := !strings.HasPrefix(downloadURL, "http://") && !strings.HasPrefix(downloadURL, "https://")
	if isRelative {
		if c.BaseURL == "" {
			return nil, fmt.Errorf("download URL %q is relative but client has no BaseURL", downloadURL)
		}
		downloadURL = c.BaseURL + downloadURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	if isRelative {
		c.setHeaders(req)
	}

	resp, err := c.HTTPClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, newHTTPError(http.MethodGet, downloadURL, resp)
	}

	const maxDownloadSize = 100 << 20 // 100 MB
	return io.ReadAll(io.LimitReader(resp.Body, maxDownloadSize))
}

// HealthCheck hits the /health endpoint and returns the response body.
func (c *APIClient) HealthCheck(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/health", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.HTTPClient.Do(req)
	err = wrapTransport(req, err)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 400 {
		return "", &HTTPError{
			Method:     http.MethodGet,
			Path:       "/health",
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(data)),
			TaskScoped: requestUsedTaskToken(resp),
		}
	}
	return strings.TrimSpace(string(data)), nil
}
