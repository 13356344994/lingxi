package exam

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Registry struct {
	Version int    `json:"version"`
	Exams   []Exam `json:"exams"`
}

func LoadRegistry(path string) (Registry, error) {
	if strings.TrimSpace(path) == "" {
		return Registry{}, errors.New("state path is required")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Registry{Version: 1}, nil
		}
		return Registry{}, err
	}
	var r Registry
	if err := json.Unmarshal(b, &r); err != nil {
		return Registry{}, err
	}
	if r.Version == 0 {
		r.Version = 1
	}
	return r, nil
}

func SaveRegistry(path string, r Registry) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("state path is required")
	}
	r.Version = 1
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (r Registry) Upsert(e Exam) Registry {
	out := Registry{Version: 1, Exams: append([]Exam(nil), r.Exams...)}
	for i := range out.Exams {
		if out.Exams[i].ExamID == e.ExamID {
			out.Exams[i] = e
			return out
		}
	}
	out.Exams = append(out.Exams, e)
	return out
}

func (r Registry) Get(examID string) (Exam, bool) {
	for _, e := range r.Exams {
		if e.ExamID == examID {
			return e, true
		}
	}
	return Exam{}, false
}

func (r Registry) StartNext(now time.Time) (Registry, Exam, bool, error) {
	next, ok := NextSealed(r.Exams)
	if !ok {
		return r, Exam{}, false, nil
	}
	started, err := Start(next, now)
	if err != nil {
		return r, Exam{}, false, err
	}
	return r.Upsert(started), started, true, nil
}
