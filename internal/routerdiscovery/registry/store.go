package registry

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

func Load(path string) (Registry, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Registry{}, nil
	}
	if err != nil {
		return Registry{}, err
	}
	var r Registry
	if err := json.Unmarshal(raw, &r); err != nil {
		return Registry{}, err
	}
	want := r.Hash
	normalized, err := finalize(r)
	if err != nil {
		return Registry{}, err
	}
	if want == "" || normalized.Hash != want {
		return Registry{}, errors.New("router registry hash mismatch")
	}
	return r, nil
}

func Save(path string, r Registry) error {
	normalized, err := finalize(r)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
