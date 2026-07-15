package binancereadonly

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOneShotOrchestrationRequestsJobExactlyOnceAcrossResume(t *testing.T) {
	root := secureRoot(t)
	now := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	jobCalls, statusCalls := 0, 0
	client := &Client{
		Credentials: Credentials{APIKey: "key", Secret: "secret"}, Now: func() time.Time { return now },
		HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			switch request.URL.Path {
			case OrderAsyncEndpoint:
				jobCalls++
				return responseFor(`{"downloadId":"raw-private-id-1"}`), nil
			case OrderAsyncStatusEndpoint:
				statusCalls++
				return responseFor(`{"downloadId":"raw-private-id-1","status":"completed","url":"https://www.binance.com/orders.zip"}`), nil
			default:
				t.Fatalf("unexpected endpoint: %s", request.URL.Path)
				return nil, nil
			}
		})},
	}
	crashed := false
	cfg := OrchestratorConfig{
		Root: root, Client: client, DownloadHostVerified: true, Now: func() time.Time { return now },
		Sleep: func(time.Duration) {},
		ProcessArtifact: func(context.Context, string) (ZeroFillSummary, error) {
			return ZeroFillSummary{Rows: 1, IdentityRows: 1, TimeRows: 1, Counts: map[string]int{"CANCELED": 1}}, nil
		},
		Crash: func(step string) error {
			if step == "after_job_persisted" && !crashed {
				crashed = true
				return errors.New("crash")
			}
			return nil
		},
	}
	if _, err := RunOneShot(context.Background(), cfg); err == nil {
		t.Fatal("injected crash was not observed")
	}
	cfg.Crash = nil
	result, err := RunOneShot(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.StopStatus != "STOP_COMPLETED_ZERO_FILL_PRESENT" || jobCalls != 1 || statusCalls != 1 {
		t.Fatalf("result=%+v jobCalls=%d statusCalls=%d", result, jobCalls, statusCalls)
	}
	assertNoRawIDOutsidePrivateRaw(t, root, "raw-private-id-1")
}

func TestIntentCrashNeverRetriesAmbiguousJob(t *testing.T) {
	root := secureRoot(t)
	jobCalls := 0
	client := fakeJobClient(t, &jobCalls, 0)
	cfg := OrchestratorConfig{
		Root: root, Client: client, DownloadHostVerified: true,
		Now:             func() time.Time { return time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC) },
		ProcessArtifact: func(context.Context, string) (ZeroFillSummary, error) { return ZeroFillSummary{}, nil },
		Crash: func(step string) error {
			if step == "after_intent" {
				return errors.New("crash")
			}
			return nil
		},
	}
	if _, err := RunOneShot(context.Background(), cfg); err == nil {
		t.Fatal("intent crash absent")
	}
	cfg.Crash = nil
	result, err := RunOneShot(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.StopStatus != "STOP_AMBIGUOUS_JOB_INTENT" || jobCalls != 0 {
		t.Fatalf("ambiguous intent retried: result=%+v calls=%d", result, jobCalls)
	}
}

func TestStatusPollingHasHardBound(t *testing.T) {
	root := secureRoot(t)
	jobCalls, statusCalls := 0, 0
	client := fakeJobClient(t, &jobCalls, http.StatusOK)
	client.HTTP = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == OrderAsyncEndpoint {
			jobCalls++
			return responseFor(`{"downloadId":"bounded-id"}`), nil
		}
		statusCalls++
		return responseFor(`{"downloadId":"bounded-id","status":"processing","url":""}`), nil
	})}
	result, err := RunOneShot(context.Background(), OrchestratorConfig{
		Root: root, Client: client, DownloadHostVerified: true,
		Now:             func() time.Time { return time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC) },
		Sleep:           func(time.Duration) {},
		ProcessArtifact: func(context.Context, string) (ZeroFillSummary, error) { return ZeroFillSummary{}, nil },
	})
	if err != nil || result.StopStatus != "STOP_STATUS_TIMEOUT" || jobCalls != 1 || statusCalls != MaxStatusPolls {
		t.Fatalf("result=%+v err=%v jobs=%d statuses=%d", result, err, jobCalls, statusCalls)
	}
}

func TestUnverifiedHostStopsBeforeRootOrQuota(t *testing.T) {
	root := filepath.Join(t.TempDir(), "must-not-create")
	result, err := RunOneShot(context.Background(), OrchestratorConfig{Root: root})
	if err != nil || result.StopStatus != "STOP_UNVERIFIED_DOWNLOAD_HOST" {
		t.Fatalf("unexpected preflight: %+v %v", result, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("unverified preflight mutated root")
	}
}

func fakeJobClient(t *testing.T, calls *int, _ int) *Client {
	t.Helper()
	now := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	return &Client{
		Credentials: Credentials{APIKey: "key", Secret: "secret"}, Now: func() time.Time { return now },
		HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			(*calls)++
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"downloadId":"id"}`)), Header: make(http.Header)}, nil
		})},
	}
}

func secureRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func assertNoRawIDOutsidePrivateRaw(t *testing.T, root, rawID string) {
	t.Helper()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || strings.Contains(path, string(filepath.Separator)+"private-raw"+string(filepath.Separator)) {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), rawID) {
			t.Fatalf("raw download ID leaked into %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
