// Package artifactstore stores bounded, content-addressed private repair inputs.
package artifactstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const MaxBytes = 8 << 20

var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Store interface {
	Put(context.Context, []byte) (string, error)
	Get(context.Context, string) ([]byte, error)
}

func Digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func checked(b []byte, key string) error {
	if len(b) == 0 || len(b) > MaxBytes || !hashPattern.MatchString(key) || Digest(b) != key {
		return errors.New("invalid artifact content/hash")
	}
	return nil
}

type Disk struct{ root string }

func NewDisk(root string) (*Disk, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("absolute private artifact root required")
	}
	if e := os.MkdirAll(root, 0700); e != nil {
		return nil, e
	}
	canonical, e := filepath.EvalSymlinks(root)
	if e != nil || canonical != filepath.Clean(root) {
		return nil, errors.New("artifact root must be canonical")
	}
	st, e := os.Stat(root)
	if e != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("artifact root must be private (0700)")
	}
	return &Disk{root: canonical}, nil
}
func (d *Disk) Get(ctx context.Context, key string) ([]byte, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if !hashPattern.MatchString(key) {
		return nil, errors.New("invalid artifact key")
	}
	path := filepath.Join(d.root, key)
	st, e := os.Lstat(path)
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() || st.Size() > MaxBytes {
		return nil, errors.New("invalid artifact file")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	return b, checked(b, key)
}
func (d *Disk) Put(ctx context.Context, b []byte) (string, error) {
	key := Digest(b)
	if e := checked(b, key); e != nil {
		return "", e
	}
	if e := ctx.Err(); e != nil {
		return "", e
	}
	temp, e := os.CreateTemp(d.root, ".upload-")
	if e != nil {
		return "", e
	}
	defer os.Remove(temp.Name())
	_, e = temp.Write(b)
	if e == nil {
		e = temp.Sync()
	}
	ce := temp.Close()
	if e != nil {
		return "", e
	}
	if ce != nil {
		return "", ce
	}
	// Link is atomic no-replace, so readers never observe a partial upload.
	e = os.Link(temp.Name(), filepath.Join(d.root, key))
	if e != nil && !os.IsExist(e) {
		return "", e
	}
	existing, e := d.Get(ctx, key)
	if e != nil || !bytes.Equal(existing, b) {
		return "", errors.New("artifact collision/corruption")
	}
	dir, e := os.Open(d.root)
	if e != nil {
		return "", e
	}
	e = dir.Sync()
	dir.Close()
	return key, e
}

type Remote struct {
	base, token string
	client      *http.Client
}

func NewRemote(base, token string, isolatedHTTP bool) (*Remote, error) {
	u, e := url.Parse(base)
	if e != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Host == "" || u.Path != "" || (u.Scheme != "https" && !(isolatedHTTP && u.Scheme == "http")) || len(token) < 24 {
		return nil, errors.New("HTTPS artifact base and private bearer token required")
	}
	return &Remote{base: strings.TrimSuffix(base, "/"), token: token, client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (r *Remote) transfer(ctx context.Context, method, key string, b []byte) ([]byte, error) {
	if !hashPattern.MatchString(key) {
		return nil, errors.New("invalid artifact key")
	}
	req, e := http.NewRequestWithContext(ctx, method, r.base+"/artifacts/"+key, bytes.NewReader(b))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	res, e := r.client.Do(req)
	if e != nil {
		return nil, errors.New("artifact transport unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("artifact service refused request")
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, MaxBytes+1))
	if e != nil {
		return nil, errors.New("artifact response incomplete")
	}
	return raw, nil
}
func (r *Remote) Put(ctx context.Context, b []byte) (string, error) {
	key := Digest(b)
	if e := checked(b, key); e != nil {
		return "", e
	}
	_, e := r.transfer(ctx, http.MethodPut, key, b)
	return key, e
}
func (r *Remote) Get(ctx context.Context, key string) ([]byte, error) {
	b, e := r.transfer(ctx, http.MethodGet, key, nil)
	if e != nil {
		return nil, e
	}
	return b, checked(b, key)
}

// Handler is intended behind private TLS or an explicitly isolated lab network.
func Handler(store Store, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(token) < 24 || r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", 401)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/artifacts/")
		if r.URL.Path != "/artifacts/"+key || !hashPattern.MatchString(key) {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			b, e := store.Get(r.Context(), key)
			if e != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write(b)
		case http.MethodPut:
			b, e := io.ReadAll(io.LimitReader(r.Body, MaxBytes+1))
			if e != nil || checked(b, key) != nil {
				http.Error(w, "invalid artifact", 400)
				return
			}
			if _, e = store.Put(r.Context(), b); e != nil {
				http.Error(w, "artifact unavailable", 503)
				return
			}
			w.WriteHeader(200)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}
