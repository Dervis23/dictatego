// Package config loads and stores user settings.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Pair mirrors the reference dictionary entry shape.
type Pair struct {
	Match   string `json:"match"`
	Replace string `json:"replace"`
}

// Config holds everything the tray app persists between runs.
// The shape stays compatible with the reference application's config.json.
type Config struct {
	APIKey        string `json:"api_key"`
	Record        string `json:"shortcut"`   // record/translate hotkeys were
	Translate     string `json:"translate"`  // called shortcut in the reference
	Language      string `json:"language"`
	Cancel        string `json:"cancel"`
	Dictionary    []Pair `json:"dictionary"`
	Style         string `json:"style"`         // "toggle" or "hold"
	Transcription string `json:"transcription"` // "smart" or "verbatim"
	Microphone    string `json:"microphone"`
}

// Dir returns the configuration directory, honouring XDG_CONFIG_HOME.
func Dir() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "dictatego"), nil
}

// Path returns the config.json path inside Dir.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads the configuration. A missing file is not an error: defaults apply.
func Load() (*Config, error) {
	cfg := &Config{Style: "toggle", Transcription: "smart"}
	path, err := Path()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("config okunamadı: %w", err)
	}
	var data struct {
		APIKey        string        `json:"api_key"`
		Record        string        `json:"shortcut"`
		Translate     string        `json:"translate"`
		Language      string        `json:"language"`
		Cancel        string        `json:"cancel"`
		Dictionary    []Pair        `json:"dictionary"`
		Style         string        `json:"style"`
		Transcription string        `json:"transcription"`
		Microphone    string        `json:"microphone"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return cfg, fmt.Errorf("config çözülemedi: %w", err)
	}
	if data.APIKey != "" {
		cfg.APIKey = data.APIKey
	}
	if data.Record != "" {
		cfg.Record = data.Record
	}
	if data.Translate != "" {
		cfg.Translate = data.Translate
	}
	if data.Language != "" {
		cfg.Language = data.Language
	}
	if data.Cancel != "" {
		cfg.Cancel = data.Cancel
	}
	if data.Dictionary != nil {
		cfg.Dictionary = data.Dictionary
	}
	switch data.Style {
	case "hold", "toggle":
		cfg.Style = data.Style
	}
	switch data.Transcription {
	case "smart", "verbatim":
		cfg.Transcription = data.Transcription
	}
	if data.Microphone != "" {
		cfg.Microphone = data.Microphone
	}
	return cfg, nil
}

// Save writes the configuration with owner-only permissions: it carries the API key.
func (c *Config) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("config dizini oluşturulamadı: %w", err)
		}
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("config yazılamadı: %w", err)
	}
	return nil
}
