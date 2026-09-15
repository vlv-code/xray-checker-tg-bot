package xray

import (
       "net/http"
       "net/http/httptest"
       "os"
       "path/filepath"
       "strings"
       "testing"
)

func validGeoPayload() []byte {
       // Larger than minDatFileSize so it counts as a valid database file.
       return []byte(strings.Repeat("geodata-payload-", 128))
}

func TestEnsureFileKeepsValidFile(t *testing.T) {
       // A server that fails loudly if reached: an existing valid-size file
       // must never trigger a re-download.
       srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
               t.Errorf("server must not be reached when a valid file exists")
               w.WriteHeader(http.StatusInternalServerError)
       }))
       defer srv.Close()

       base := t.TempDir()
       target := filepath.Join(base, "geo", "geosite.dat")
       if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
               t.Fatal(err)
       }
       if err := os.WriteFile(target, validGeoPayload(), 0644); err != nil {
               t.Fatal(err)
       }

       gfm := NewGeoFileManager(base)
       if err := gfm.ensureFile("geo/geosite.dat", srv.URL); err != nil {
               t.Fatalf("ensureFile failed: %v", err)
       }

       data, err := os.ReadFile(target)
       if err != nil {
               t.Fatal(err)
       }
       if string(data) != string(validGeoPayload()) {
               t.Error("existing valid file was modified")
       }
}

func TestEnsureFileRedownloadsCorruptedFile(t *testing.T) {
       srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
               w.Write(validGeoPayload())
       }))
       defer srv.Close()

       base := t.TempDir()
       target := filepath.Join(base, "geo", "geosite.dat")
       if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
               t.Fatal(err)
       }
       // Truncated leftover from an interrupted download of a pre-atomic version.
       if err := os.WriteFile(target, []byte("junk"), 0644); err != nil {
               t.Fatal(err)
       }

       gfm := NewGeoFileManager(base)
       if err := gfm.ensureFile("geo/geosite.dat", srv.URL); err != nil {
               t.Fatalf("ensureFile failed: %v", err)
       }

       data, err := os.ReadFile(target)
       if err != nil {
               t.Fatal(err)
       }
       if len(data) < minDatFileSize {
               t.Errorf("corrupted file was not re-downloaded: %d bytes", len(data))
       }
}

func TestDownloadFileAtomicOnInterruptedBody(t *testing.T) {
       // Server claims a large body but closes the connection early: the copy
       // must fail and leave neither the final file nor a .tmp leftover.
       srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
               w.Header().Set("Content-Length", "1048576")
               w.(http.Flusher).Flush()
               w.Write(validGeoPayload()[:512])
       }))
       defer srv.Close()

       base := t.TempDir()
       target := filepath.Join(base, "geo", "geoip.dat")
       if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
               t.Fatal(err)
       }

       gfm := NewGeoFileManager(base)
       err := gfm.downloadFile(srv.URL, target)
       if err == nil {
               t.Fatal("expected an error from an interrupted body")
       }
       if !strings.Contains(err.Error(), "failed to write file") {
               t.Fatalf("error must come from the interrupted body, got: %v", err)
       }

       if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
               t.Error("final file must not exist after a failed download")
       }
       if _, statErr := os.Stat(target + ".tmp"); !os.IsNotExist(statErr) {
               t.Error(".tmp leftover must be cleaned up after a failed download")
       }
}

func TestDownloadFileAtomicRename(t *testing.T) {
       srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
               w.Write(validGeoPayload())
       }))
       defer srv.Close()

       base := t.TempDir()
       target := filepath.Join(base, "geo", "geoip.dat")
       if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
               t.Fatal(err)
       }

       gfm := NewGeoFileManager(base)
       if err := gfm.downloadFile(srv.URL, target); err != nil {
               t.Fatalf("downloadFile failed: %v", err)
       }

       data, err := os.ReadFile(target)
       if err != nil {
               t.Fatal(err)
       }
       if string(data) != string(validGeoPayload()) {
               t.Error("downloaded content mismatch")
       }
       if _, statErr := os.Stat(target + ".tmp"); !os.IsNotExist(statErr) {
               t.Error(".tmp file must be renamed away after a successful download")
       }
}
