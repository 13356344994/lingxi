package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRotationStatusAPI_NilRunner(t *testing.T) {
	s := &DashboardServer{}
	req := httptest.NewRequest(http.MethodGet, "/api/rotation/status", nil)
	rec := httptest.NewRecorder()
	s.handleRotationStatusAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var st RotationStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("json: %v", err)
	}
	if st.Active {
		t.Fatalf("nil runner should return inactive status: %+v", st)
	}
}

func TestRotationControlAPI_RequiresRunner(t *testing.T) {
	s := &DashboardServer{}
	startBody, _ := json.Marshal(RotationStartRequest{Confirm: "LIVE"})
	for _, tc := range []struct {
		name    string
		method  string
		path    string
		body    []byte
		handler func(http.ResponseWriter, *http.Request)
	}{
		{"start", http.MethodPost, "/api/rotation/start", startBody, s.handleRotationStartAPI},
		{"stop", http.MethodPost, "/api/rotation/stop", nil, s.handleRotationStopAPI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reader *bytes.Reader
			if tc.body != nil {
				reader = bytes.NewReader(tc.body)
			} else {
				reader = bytes.NewReader(nil)
			}
			req := httptest.NewRequest(tc.method, tc.path, reader)
			req.RemoteAddr = "127.0.0.1:12345"
			rec := httptest.NewRecorder()
			tc.handler(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s status=%d body=%s", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRotationStartAPI_RequiresConfirm(t *testing.T) {
	runner := testUnifiedRotationRunnerWithFakes(t)
	runner.SetActive(false)
	s := &DashboardServer{rotationRunner: runner}

	body, _ := json.Marshal(RotationStartRequest{})
	req := httptest.NewRequest(http.MethodPost, "/api/rotation/start", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	s.handleRotationStartAPI(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	if runner.Status().Active {
		t.Fatal("start must not activate without confirm=LIVE")
	}
}

func TestRotationControlAPI_WithRunner(t *testing.T) {
	runner := testUnifiedRotationRunnerWithFakes(t)
	runner.SetActive(false)
	s := &DashboardServer{rotationRunner: runner}

	body, _ := json.Marshal(RotationStartRequest{Confirm: "LIVE"})
	req := httptest.NewRequest(http.MethodPost, "/api/rotation/start", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	s.handleRotationStartAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", rec.Code, rec.Body.String())
	}
	var st RotationStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !st.Active {
		t.Fatalf("start should set active=true: %+v", st)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/rotation/stop", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec = httptest.NewRecorder()
	s.handleRotationStopAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("stop status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("json: %v", err)
	}
	if st.Active {
		t.Fatalf("stop should set active=false: %+v", st)
	}
}
