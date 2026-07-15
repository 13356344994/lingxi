package historydownload

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

func TestTokenSaltConcurrentGoroutinesShareExactSalt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.salt")
	store := TokenStore{SaltPath: path}
	const workers = 32
	results := make(chan string, workers)
	errorsFound := make(chan error, workers)
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			token, err := store.Token("same-id")
			if err != nil {
				errorsFound <- err
				return
			}
			results <- token
		}()
	}
	group.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	var first string
	for result := range results {
		if first == "" {
			first = result
		}
		if result != first {
			t.Fatal("concurrent token calls used different salts")
		}
	}
	assertValidSaltFile(t, path)
}

func TestTokenSaltConcurrentProcessesConverge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.salt")
	commands := make([]*exec.Cmd, 2)
	outputs := make([]bytes.Buffer, 2)
	for index := range commands {
		command := exec.Command(os.Args[0], "-test.run=^TestTokenSaltProcessHelper$")
		command.Env = append(os.Environ(), "HISTORYDOWNLOAD_SALT_HELPER=1", "HISTORYDOWNLOAD_SALT_PATH="+path)
		command.Stdout = &outputs[index]
		command.Stderr = &outputs[index]
		commands[index] = command
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for index, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("salt helper failed: %v %s", err, outputs[index].String())
		}
	}
	assertValidSaltFile(t, path)
}

func TestTokenSaltProcessHelper(t *testing.T) {
	if os.Getenv("HISTORYDOWNLOAD_SALT_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	if _, err := (TokenStore{SaltPath: os.Getenv("HISTORYDOWNLOAD_SALT_PATH")}).Token("same-id"); err != nil {
		t.Fatal(err)
	}
}

func TestTokenSaltRejectsUnsafeMetadata(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{"mode-0644", func(t *testing.T, path string) {
			if err := os.WriteFile(path, bytes.Repeat([]byte{1}, 32), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"short", func(t *testing.T, path string) {
			if err := os.WriteFile(path, bytes.Repeat([]byte{1}, 31), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"long", func(t *testing.T, path string) {
			if err := os.WriteFile(path, bytes.Repeat([]byte{1}, 33), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink", func(t *testing.T, path string) {
			target := path + ".target"
			if err := os.WriteFile(target, bytes.Repeat([]byte{1}, 32), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), test.name+".salt")
			test.setup(t, path)
			if _, err := (TokenStore{SaltPath: path}).Token("id"); err == nil {
				t.Fatal("unsafe salt accepted")
			}
		})
	}
}

func assertValidSaltFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != 32 {
		t.Fatalf("invalid salt metadata: mode=%v size=%d", info.Mode(), info.Size())
	}
}
