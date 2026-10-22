package artifactstore

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateDiskAndRemoteIntegrity(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "artifacts")
	d, e := NewDisk(root)
	if e != nil {
		t.Fatal(e)
	}
	b := []byte("synthetic candidate")
	key, e := d.Put(ctx, b)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = d.Put(ctx, b); e != nil {
		t.Fatal(e)
	}
	token := strings.Repeat("x", 32)
	server := httptest.NewServer(Handler(d, token))
	defer server.Close()
	r, e := NewRemote(server.URL, token, true)
	if e != nil {
		t.Fatal(e)
	}
	got, e := r.Get(ctx, key)
	if e != nil || !bytes.Equal(got, b) {
		t.Fatal(e)
	}
	res, e := http.Get(server.URL + "/artifacts/" + key)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("unauthenticated artifact read")
	}
	if _, e = r.Get(ctx, "../secret"); e == nil {
		t.Fatal("path escaped")
	}
	if _, e = NewRemote(server.URL, token, false); e == nil {
		t.Fatal("HTTP accepted without isolated lab opt-in")
	}
	if e = os.WriteFile(filepath.Join(root, key), []byte("corrupt"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Get(ctx, key); e == nil {
		t.Fatal("corruption accepted")
	}
}
