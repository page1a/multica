// Package localoutputs keeps this machine's copy of every attachment an agent
// on it uploaded, keyed by attachment id, so the desktop app can open the file
// in place instead of downloading it back from the server.
//
// "This file is on this computer" is only true here, so none of it goes to the
// server: the store lives under the daemon's profile directory and is only
// reachable through the daemon's 127.0.0.1 listener.
//
// Layout: <root>/<attachment-id>/meta.json and <root>/<attachment-id>/<filename>.
// The meta records size and SHA-256 at write time; a lookup only answers with
// a path while the file on disk still matches it, so a copy that was deleted
// or edited falls back to the server download.
package localoutputs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const metaName = "meta.json"

// ErrNotFound means the store holds no usable copy for the id: never written,
// pruned, removed, or changed since it was written.
var ErrNotFound = errors.New("no local copy")

// ErrInvalidID rejects anything that is not an attachment id, so a caller can
// never steer the store at a path.
var ErrInvalidID = errors.New("not an attachment id")

var idPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidID reports whether id has the shape of an attachment id (a UUID).
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Entry is what the store knows about one copy.
type Entry struct {
	AttachmentID string    `json:"attachment_id"`
	Filename     string    `json:"filename"`
	SizeBytes    int64     `json:"size_bytes"`
	SHA256       string    `json:"sha256"`
	CreatedAt    time.Time `json:"created_at"`
	// ModTime is the file's mtime right after it was written. A lookup that
	// sees the same size and mtime trusts the recorded hash instead of reading
	// the whole file again; anything else is re-hashed.
	ModTime time.Time `json:"mod_time"`
	// Path is filled in by Lookup and never persisted.
	Path string `json:"-"`
}

// Store is one profile's copy library.
type Store struct {
	Root string
	Now  func() time.Time
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// cleanFilename reduces a caller-supplied name to a single path element that
// cannot climb out of the entry directory or collide with the meta file.
func cleanFilename(name string) (string, error) {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	name = filepath.Base(name)
	if strings.Trim(name, "./") == "" || name == metaName {
		return "", fmt.Errorf("invalid filename %q", name)
	}
	return name, nil
}

// Put writes r as the copy for id, replacing any earlier copy.
func (s *Store) Put(id, filename string, r io.Reader) (Entry, error) {
	if !ValidID(id) {
		return Entry{}, ErrInvalidID
	}
	name, err := cleanFilename(filename)
	if err != nil {
		return Entry{}, err
	}
	dir := filepath.Join(s.Root, strings.ToLower(id))
	if err := os.RemoveAll(dir); err != nil {
		return Entry{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Entry{}, err
	}
	target := filepath.Join(dir, name)
	tmp, err := os.CreateTemp(dir, ".incoming-*")
	if err != nil {
		return Entry{}, err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(tmp, hash), r)
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.RemoveAll(dir)
		return Entry{}, errors.Join(copyErr, closeErr)
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		_ = os.RemoveAll(dir)
		return Entry{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		_ = os.RemoveAll(dir)
		return Entry{}, err
	}
	entry := Entry{
		AttachmentID: strings.ToLower(id),
		Filename:     name,
		SizeBytes:    size,
		SHA256:       hex.EncodeToString(hash.Sum(nil)),
		CreatedAt:    s.now().UTC(),
		ModTime:      info.ModTime().UTC(),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		_ = os.RemoveAll(dir)
		return Entry{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, metaName), data, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return Entry{}, err
	}
	entry.Path = target
	return entry, nil
}

func (s *Store) readMeta(dir string) (Entry, error) {
	data, err := os.ReadFile(filepath.Join(dir, metaName))
	if err != nil {
		return Entry{}, err
	}
	var entry Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// Lookup returns the copy for id with its absolute path, or ErrNotFound when
// there is none or the file no longer matches what was written.
func (s *Store) Lookup(id string) (Entry, error) {
	if !ValidID(id) {
		return Entry{}, ErrInvalidID
	}
	dir := filepath.Join(s.Root, strings.ToLower(id))
	entry, err := s.readMeta(dir)
	if err != nil {
		return Entry{}, ErrNotFound
	}
	name, err := cleanFilename(entry.Filename)
	if err != nil || name != entry.Filename {
		return Entry{}, ErrNotFound
	}
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != entry.SizeBytes {
		return Entry{}, ErrNotFound
	}
	if !info.ModTime().UTC().Equal(entry.ModTime) {
		sum, err := hashFile(path)
		if err != nil || sum != entry.SHA256 {
			return Entry{}, ErrNotFound
		}
	}
	entry.Path = path
	return entry, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Prune removes copies written more than ttl ago, and entry directories with
// no readable meta once their own mtime is that old. ttl <= 0 keeps everything.
// Returns how many entries were removed and the bytes they held.
func (s *Store) Prune(ttl time.Duration) (removed int, bytes int64) {
	if ttl <= 0 {
		return 0, 0
	}
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return 0, 0
	}
	cutoff := s.now().Add(-ttl)
	for _, e := range entries {
		// Only directories this store created: anything else in the root is
		// left alone.
		if !e.IsDir() || !ValidID(e.Name()) {
			continue
		}
		dir := filepath.Join(s.Root, e.Name())
		written := time.Time{}
		if entry, err := s.readMeta(dir); err == nil {
			written = entry.CreatedAt
		} else if info, err := e.Info(); err == nil {
			written = info.ModTime()
		}
		if written.IsZero() || written.After(cutoff) {
			continue
		}
		size := dirSize(dir)
		if err := os.RemoveAll(dir); err == nil {
			removed++
			bytes += size
		}
	}
	return removed, bytes
}

func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
