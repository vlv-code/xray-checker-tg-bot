package asn

import (
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func gzServe(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		gz := gzip.NewWriter(w)
		gz.Write(payload)
		gz.Close()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEnsureDBDownloadsAndGunzips(t *testing.T) {
	payload := []byte("pretend this is an mmdb blob")
	srv := gzServe(t, payload)
	path := filepath.Join(t.TempDir(), "asn.mmdb")

	if err := EnsureDB(path, srv.URL); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Error("gunzipped content mismatch")
	}

	// Existing file: no second download.
	calls := 0
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte("should not be fetched"))
	}))
	defer srv2.Close()
	if err := EnsureDB(path, srv2.URL); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Error("existing DB must not be re-downloaded")
	}
}

func TestEnsureDBBadURL(t *testing.T) {
	if err := EnsureDB(filepath.Join(t.TempDir(), "x.mmdb"), "http://127.0.0.1:1/nope"); err == nil {
		t.Error("unreachable URL must error")
	}
}

func TestFormatASN(t *testing.T) {
	if got := formatASN(9009, "M247 Europe SRL"); got != "AS9009 M247 Europe SRL" {
		t.Errorf("formatASN: %q", got)
	}
	if got := formatASN(0, ""); got != "" {
		t.Errorf("empty ASN must format to empty string, got %q", got)
	}
}

func TestLookupInvalidIP(t *testing.T) {
	db := &DB{}
	if got := db.Lookup("not-an-ip"); got != "" {
		t.Errorf("invalid IP must return empty, got %q", got)
	}
	if got := db.Lookup("192.168.1.1"); got != "" {
		t.Errorf("private IP must return empty, got %q", got)
	}
}

// Optional: against a real database when provided.
func TestLookupRealDB(t *testing.T) {
	path := os.Getenv("ASN_TEST_DB")
	if path == "" {
		t.Skip("set ASN_TEST_DB to a real dbip-asn-lite mmdb to run")
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := db.Lookup("8.8.8.8"); got == "" {
		t.Log("8.8.8.8 resolved to empty; check the database covers it")
	}
}
