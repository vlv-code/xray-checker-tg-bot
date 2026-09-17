package nodes

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReporterSend(t *testing.T) {
	var gotAuth, gotPath, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		w.Write([]byte(`{"managedSubs":["https://sub.example/one"]}`))
	}))
	defer srv.Close()

	rep := NewReporter(srv.URL+"/api/v1/nodes/report", "secrettoken")
	managed, err := rep.Send(ReportPayload{Version: "v", CheckIntervalSec: 60})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/nodes/report" {
		t.Errorf("path: %s", gotPath)
	}
	if gotAuth != "Bearer secrettoken" {
		t.Errorf("auth header: %s", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type: %s", gotCT)
	}
	if len(managed) != 1 || managed[0] != "https://sub.example/one" {
		t.Errorf("managed list: %v", managed)
	}
}

func TestReporterSendErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	rep := NewReporter(srv.URL, "tok")
	if _, err := rep.Send(ReportPayload{}); err == nil {
		t.Error("401 must be an error")
	}
	if _, err := NewReporter("http://127.0.0.1:1/", "tok").Send(ReportPayload{}); err == nil {
		t.Error("connection failure must be an error")
	}
}
