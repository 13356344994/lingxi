package historydownload

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Credentials deliberately keeps secrets unexported from formatting helpers.
type Credentials struct {
	Key        string
	Secret     string
	Passphrase string
}

func (c Credentials) Complete() bool {
	return strings.TrimSpace(c.Key) != "" && strings.TrimSpace(c.Secret) != "" && strings.TrimSpace(c.Passphrase) != ""
}

// LoadCredentials selects only secret.yaml's top-level okx mapping. Flat
// production files are rejected so another venue's credentials cannot be used.
func LoadCredentials(path string) (Credentials, []string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Credentials{}, nil, err
	}
	var warnings []string
	if info.Mode().Perm()&0o077 != 0 {
		warnings = append(warnings, "credential file is group/world readable; tracked-file permissions require review")
	}
	warnings = append(warnings, "credential capability is unverified; trade permission does not block the GET-only downloader")
	b, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, warnings, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(b, &root); err != nil {
		return Credentials{}, warnings, errors.New("historydownload: invalid credential YAML")
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return Credentials{}, warnings, errors.New("historydownload: credential root must be mapping")
	}
	mapping := root.Content[0]
	selected, err := selectCredentialMapping(mapping)
	if err != nil {
		return Credentials{}, warnings, err
	}
	values := map[string]string{}
	for i := 0; i < len(selected.Content); i += 2 {
		key, value := selected.Content[i], selected.Content[i+1]
		if key.Kind != yaml.ScalarNode || value.Kind != yaml.ScalarNode {
			return Credentials{}, warnings, errors.New("historydownload: credential values must be scalar")
		}
		if key.Value != "api" && key.Value != "secret" && key.Value != "passphrase" {
			return Credentials{}, warnings, fmt.Errorf("historydownload: unsupported okx credential field %q", key.Value)
		}
		if _, duplicate := values[key.Value]; duplicate {
			return Credentials{}, warnings, errors.New("historydownload: duplicate credential field")
		}
		values[key.Value] = value.Value
	}
	c := Credentials{Key: values["api"], Secret: values["secret"], Passphrase: values["passphrase"]}
	if !c.Complete() {
		return Credentials{}, warnings, errors.New("historydownload: credential fields incomplete")
	}
	return c, warnings, nil
}

func selectCredentialMapping(root *yaml.Node) (*yaml.Node, error) {
	top := map[string]*yaml.Node{}
	for i := 0; i < len(root.Content); i += 2 {
		key, value := root.Content[i], root.Content[i+1]
		if _, duplicate := top[key.Value]; duplicate {
			return nil, errors.New("historydownload: duplicate top-level credential key")
		}
		top[key.Value] = value
	}
	if okx, found := top["okx"]; found {
		if okx.Kind != yaml.MappingNode {
			return nil, errors.New("historydownload: okx credentials must be mapping")
		}
		return okx, nil
	}
	return nil, errors.New("historydownload: top-level okx mapping required")
}

// Limiter is a cancellable minimum-interval limiter for bounded polite polling.
type Limiter struct {
	mu       sync.Mutex
	interval time.Duration
	last     time.Time
}

func NewLimiter(interval time.Duration) (*Limiter, error) {
	if interval < 0 {
		return nil, fmt.Errorf("historydownload: negative rate interval")
	}
	return &Limiter{interval: interval}, nil
}

func (l *Limiter) Wait() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if wait := l.interval - time.Since(l.last); wait > 0 {
		time.Sleep(wait)
	}
	l.last = time.Now()
}
