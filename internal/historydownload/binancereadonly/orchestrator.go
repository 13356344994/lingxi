package binancereadonly

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"syscall"
	"time"
)

const (
	MaxStatusPolls     = 4
	StatusPollInterval = 30 * time.Second
	MaxStatusDuration  = 2 * time.Minute
)

// OfficialDownloadHostVerified remains false because the current official
// endpoint page describes the URL field but does not normatively pin its host.
// The production CLI cannot override this value.
const OfficialDownloadHostVerified = false

type ArtifactProcessor func(context.Context, string) (ZeroFillSummary, error)
type CrashHook func(string) error

type OrchestratorConfig struct {
	Root                 string
	Client               *Client
	ProcessArtifact      ArtifactProcessor
	DownloadHostVerified bool
	Sleep                func(time.Duration)
	Now                  func() time.Time
	Crash                CrashHook
}

type RunResult struct {
	StopStatus string
	Current    *Current
}

// RunOneShot is the only connected orchestration surface. The irreversible
// intent is synced before the quota-bearing GET. Once an intent exists without
// a hash-verified raw job response, recovery terminates ambiguously and never
// retries the job request.
func RunOneShot(ctx context.Context, cfg OrchestratorConfig) (RunResult, error) {
	if !cfg.DownloadHostVerified {
		return RunResult{StopStatus: "STOP_UNVERIFIED_DOWNLOAD_HOST"}, nil
	}
	if cfg.Client == nil {
		return RunResult{}, errors.New("binancereadonly: complete orchestrator dependencies required")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	if PilotEnd.After(now().UTC().Add(-CompletedWindowBuffer)) {
		return RunResult{}, errors.New("binancereadonly: pilot window is not complete")
	}
	sleep := cfg.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	authority, err := acquireAuthority(cfg.Root)
	if err != nil {
		return RunResult{}, err
	}
	defer authority.close()
	if err := requireCapacity(authority.root, 0); err != nil {
		return RunResult{}, err
	}
	base, err := mkdirAt(authority.root, pilotDirectory)
	if err != nil {
		return RunResult{}, err
	}
	defer base.Close()
	if current, found, err := loadCurrent(base); err != nil {
		return RunResult{}, err
	} else if found {
		return RunResult{StopStatus: current.StopStatus, Current: &current}, nil
	}
	key, err := loadHMAC(authority.root)
	if err != nil {
		return RunResult{}, err
	}
	checkpoint, hasCheckpoint, err := loadCheckpoint(base)
	if err != nil {
		return RunResult{}, err
	}
	intentExists, err := existsAt(base, "job_intent.json")
	if err != nil {
		return RunResult{}, err
	}
	freshIntent := false
	if !intentExists {
		if err := requireCapacity(authority.root, 64<<10); err != nil {
			return RunResult{}, err
		}
		if _, err := writeIntent(base); err != nil {
			return RunResult{}, err
		}
		if err := writeCheckpoint(base, Checkpoint{State: "INTENT_WRITTEN"}); err != nil {
			return RunResult{}, err
		}
		freshIntent = true
		if err := crash(cfg.Crash, "after_intent"); err != nil {
			return RunResult{}, err
		}
	} else if !hasCheckpoint || checkpoint.State == "INTENT_WRITTEN" {
		current, err := terminal(authority.root, base, PilotReceipt{
			WindowStart: PilotStart, WindowEnd: PilotEnd, QuotaSideEffect: "JOB_REQUEST_AMBIGUOUS",
			StopStatus: "STOP_AMBIGUOUS_JOB_INTENT",
		})
		return RunResult{StopStatus: "STOP_AMBIGUOUS_JOB_INTENT", Current: &current}, err
	}

	var downloadID, token, rawHash string
	if freshIntent {
		job, err := cfg.Client.RequestOrderHistory(ctx, PilotStart, PilotEnd, DefaultRecvWindow)
		if err != nil {
			current, publishErr := terminal(authority.root, base, PilotReceipt{
				WindowStart: PilotStart, WindowEnd: PilotEnd, QuotaSideEffect: "ASYNC_ORDER_HISTORY_JOB_ATTEMPTED",
				StopStatus: "STOP_ENDPOINT_REJECTED",
			})
			if publishErr != nil {
				return RunResult{}, publishErr
			}
			return RunResult{StopStatus: current.StopStatus, Current: &current}, nil
		}
		downloadID, rawHash = job.DownloadID, hashBytes(job.Raw)
		token = idToken(key, downloadID)
		if err := requireCapacity(authority.root, int64(len(job.Raw))+(64<<10)); err != nil {
			return RunResult{}, err
		}
		if err := writeRawJob(base, job.Raw); err != nil {
			return RunResult{}, err
		}
		if err := writeCheckpoint(base, Checkpoint{
			State: "JOB_REQUESTED", DownloadToken: token, RawJobHash: rawHash,
		}); err != nil {
			return RunResult{}, err
		}
		if err := crash(cfg.Crash, "after_job_persisted"); err != nil {
			return RunResult{}, err
		}
	} else {
		downloadID, token, rawHash, err = recoverDownloadID(base, key, checkpoint)
		if err != nil {
			return RunResult{}, err
		}
	}

	started := now()
	polls := checkpoint.StatusPolls
	for polls < MaxStatusPolls && now().Sub(started) <= MaxStatusDuration {
		status, err := cfg.Client.GetDownloadStatus(ctx, downloadID, DefaultRecvWindow)
		polls++
		if err != nil {
			current, publishErr := terminal(authority.root, base, terminalReceipt(token, "STOP_STATUS_FAILED", nil))
			if publishErr != nil {
				return RunResult{}, publishErr
			}
			return RunResult{StopStatus: current.StopStatus, Current: &current}, nil
		}
		if err := requireCapacity(authority.root, 64<<10); err != nil {
			return RunResult{}, err
		}
		if err := writeCheckpoint(base, Checkpoint{
			State: "STATUS_POLLED", DownloadToken: token, RawJobHash: rawHash, StatusPolls: polls,
		}); err != nil {
			return RunResult{}, err
		}
		if err := crash(cfg.Crash, "after_status_checkpoint"); err != nil {
			return RunResult{}, err
		}
		switch status.Status {
		case "completed":
			if status.URL == "" {
				return RunResult{}, errors.New("binancereadonly: completed status lacks URL")
			}
			var summary ZeroFillSummary
			if cfg.ProcessArtifact != nil {
				summary, err = cfg.ProcessArtifact(ctx, status.URL)
			} else {
				summary, err = processArtifactFD(ctx, cfg.Client.HTTP, status.URL, authority.root, base)
			}
			if err != nil {
				return RunResult{}, err
			}
			stop := "STOP_COMPLETED_ZERO_FILL_PRESENT"
			if summary.Rows == 0 {
				stop = "STOP_NO_ACTIVITY"
			} else if summary.Counts["CANCELED"]+summary.Counts["EXPIRED"] == 0 {
				stop = "STOP_NO_ZERO_FILL"
			}
			current, err := terminal(authority.root, base, terminalReceipt(token, stop, &summary))
			return RunResult{StopStatus: stop, Current: &current}, err
		case "processing":
			if polls < MaxStatusPolls {
				sleep(StatusPollInterval)
			}
		default:
			current, err := terminal(authority.root, base, terminalReceipt(token, "STOP_STATUS_FAILED", nil))
			return RunResult{StopStatus: current.StopStatus, Current: &current}, err
		}
	}
	current, err := terminal(authority.root, base, terminalReceipt(token, "STOP_STATUS_TIMEOUT", nil))
	return RunResult{StopStatus: current.StopStatus, Current: &current}, err
}

func terminalReceipt(token, stop string, summary *ZeroFillSummary) PilotReceipt {
	return PilotReceipt{
		WindowStart: PilotStart, WindowEnd: PilotEnd, DownloadToken: token,
		QuotaSideEffect: "ASYNC_ORDER_HISTORY_JOB_REQUESTED", StopStatus: stop, Summary: summary,
	}
}

func terminal(root, base *os.File, receipt PilotReceipt) (Current, error) {
	if err := requireCapacity(root, 1<<20); err != nil {
		return Current{}, err
	}
	return publishTerminal(base, receipt)
}

func writeRawJob(base *os.File, raw []byte) error {
	if len(raw) == 0 || len(raw) > 1<<20 {
		return errors.New("binancereadonly: invalid raw job response")
	}
	rawDir, err := mkdirAt(base, "private-raw")
	if err != nil {
		return err
	}
	defer rawDir.Close()
	fd, err := syscall.Openat(int(rawDir.Fd()), "job-response.json", syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "job-response.json")
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return rawDir.Sync()
}

func recoverDownloadID(base *os.File, key []byte, checkpoint Checkpoint) (string, string, string, error) {
	rawDir, err := mkdirAt(base, "private-raw")
	if err != nil {
		return "", "", "", err
	}
	defer rawDir.Close()
	raw, err := readAt(rawDir, "job-response.json", 1<<20)
	if err != nil || hashBytes(raw) != checkpoint.RawJobHash {
		return "", "", "", errors.New("binancereadonly: raw job response integrity mismatch")
	}
	var envelope struct {
		DownloadID string `json:"downloadId"`
	}
	if json.Unmarshal(raw, &envelope) != nil || !safeID(envelope.DownloadID) {
		return "", "", "", errors.New("binancereadonly: raw job response invalid")
	}
	token := idToken(key, envelope.DownloadID)
	if token != checkpoint.DownloadToken {
		return "", "", "", errors.New("binancereadonly: download token mismatch")
	}
	return envelope.DownloadID, token, checkpoint.RawJobHash, nil
}

func crash(hook CrashHook, step string) error {
	if hook == nil {
		return nil
	}
	return hook(step)
}
