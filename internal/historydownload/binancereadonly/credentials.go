package binancereadonly

import (
	"errors"
	"os"

	"gopkg.in/yaml.v3"
)

// LoadCredentials prefers an explicit top-level Binance mapping, then supports
// the repository's legacy flat api/secret shape. It never selects another
// venue's nested credentials.
func LoadCredentials(path string) (Credentials, []string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Credentials{}, nil, err
	}
	warnings := []string{"credential capability is unverified; the client has only two GET methods"}
	if info.Mode().Perm()&0o077 != 0 {
		warnings = append(warnings, "credential file is group/world readable; permissions require review")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, warnings, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return Credentials{}, warnings, errors.New("binancereadonly: invalid credential YAML")
	}
	top := root.Content[0]
	var selected *yaml.Node
	topValues := map[string]*yaml.Node{}
	for i := 0; i < len(top.Content); i += 2 {
		key, value := top.Content[i], top.Content[i+1]
		if _, duplicate := topValues[key.Value]; duplicate {
			return Credentials{}, warnings, errors.New("binancereadonly: duplicate top-level credential field")
		}
		topValues[key.Value] = value
		if key.Value == "binance" {
			if selected != nil || value.Kind != yaml.MappingNode {
				return Credentials{}, warnings, errors.New("binancereadonly: top-level binance mapping required")
			}
			selected = value
		}
	}
	if selected == nil {
		api, apiOK := topValues["api"]
		secret, secretOK := topValues["secret"]
		if !apiOK || !secretOK || api.Kind != yaml.ScalarNode || secret.Kind != yaml.ScalarNode {
			return Credentials{}, warnings, errors.New("binancereadonly: binance or legacy flat api/secret required")
		}
		credentials := Credentials{APIKey: api.Value, Secret: secret.Value}
		if !credentials.Complete() {
			return Credentials{}, warnings, errors.New("binancereadonly: credential fields incomplete")
		}
		warnings = append(warnings, "legacy flat Binance credentials selected")
		return credentials, warnings, nil
	}
	values := map[string]string{}
	for i := 0; i < len(selected.Content); i += 2 {
		key, value := selected.Content[i], selected.Content[i+1]
		if key.Kind != yaml.ScalarNode || value.Kind != yaml.ScalarNode || (key.Value != "api" && key.Value != "secret") {
			return Credentials{}, warnings, errors.New("binancereadonly: unsupported binance credential field")
		}
		if _, duplicate := values[key.Value]; duplicate {
			return Credentials{}, warnings, errors.New("binancereadonly: duplicate credential field")
		}
		values[key.Value] = value.Value
	}
	credentials := Credentials{APIKey: values["api"], Secret: values["secret"]}
	if !credentials.Complete() {
		return Credentials{}, warnings, errors.New("binancereadonly: credential fields incomplete")
	}
	return credentials, warnings, nil
}
