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
	if st.Active || st.Live {
		t.Fatalf("nil runner should return inactive status: %+v", st)
	}
}

func TestRotationControlAPI_RequiresRunner(t *testing.T) {
	s := &DashboardServer{}
	body, _ := json.Marshal(RotationModeRequest{Live: true, Confirm: "LIVE"})
	for _, tc := range []struct {
		name    string
		method  string
		path    string
		body    []byte
		handler func(http.ResponseWriter, *http.Request)
	}{
		{"start", http.MethodPost, "/api/rotation/start", nil, s.handleRotationStartAPI},
		{"stop", http.MethodPost, "/api/rotation/stop", nil, s.handleRotationStopAPI},
		{"mode", http.MethodPost, "/api/rotation/mode", body, s.handleRotationModeAPI},
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

func TestRotationControlAPI_WithRunner(t *testing.T) {
	runner := testUnifiedRotationRunnerWithFakes(t)
	runner.SetActive(false)
	runner.SetLive(false)
	s := &DashboardServer{rotationRunner: runner}

	req := httptest.NewRequest(http.MethodPost, "/api/rotation/start", nil)
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

	body, _ := json.Marshal(RotationModeRequest{Live: true, Confirm: "LIVE"})
	req = httptest.NewRequest(http.MethodPost, "/api/rotation/mode", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	rec = httptest.NewRecorder()
	s.handleRotationModeAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("mode status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("json: %v", err)
	}
	if !st.Live {
		t.Fatalf("mode should set live=true: %+v", st)
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

func TestRotationModeAPI_LiveRequiresConfirm(t *testing.T) {
	runner := testUnifiedRotationRunnerWithFakes(t)
	runner.SetLive(false)
	s := &DashboardServer{rotationRunner: runner}

	body, _ := json.Marshal(RotationModeRequest{Live: true})
	req := httptest.NewRequest(http.MethodPost, "/api/rotation/mode", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	s.handleRotationModeAPI(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	if runner.Status().Live {
		t.Fatal("live mode must not change without confirm")
	}
}
