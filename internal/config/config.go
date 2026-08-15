// Package config manages configuration and token storage for pi-google-services.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	AppName   = "pi-google-services"
	ConfigDir = ".config/" + AppName
	TokenFile = "tokens.json"
	CredFile  = "credentials.json"
)

// Credentials represents the OAuth client credentials file downloaded from GC
type Credentials struct {
	Installed InstalledConfig `json:"installed"`
	Web       InstalledConfig `json:"web"`
}

type InstalledConfig struct {
	ClientID                string   `json:"client_id"`
	ProjectID               string   `json:"project_id"`
	AuthURI                 string   `json:"auth_uri"`
	TokenURI                string   `json:"token_uri"`
	AuthProviderX509CertURL string   `json:"auth_provider_x509_cert_url"`
	ClientSecret            string   `json:"client_secret"`
	RedirectURIs            []string `json:"redirect_uris"`
}

// AppConfig returns the effective OAuth client configuration for this
// credentials file. Desktop credentials use the "installed" block; some
// downloads only populate "web", so fall back to it when installed is empty.
func (c *Credentials) AppConfig() InstalledConfig {
	if c.Installed.ClientID != "" {
		return c.Installed
	}
	return c.Web
}

// Tokens stores OAuth2 tokens persistently.
type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	Expiry       string `json:"expiry,omitempty"`
}

// Dir returns the config directory path. Override DirFn in tests.
var Dir = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ConfigDir), nil
}

// LoadCredentials reads a Google-provided credentials JSON file from disk.
func LoadCredentials(path string) (*Credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read credentials: %w", err)
	}
	return LoadCredentialsFromBytes(data)
}

// LoadCredentialsFromBytes parses Google credentials JSON from raw bytes.
func LoadCredentialsFromBytes(data []byte) (*Credentials, error) {
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	return &creds, nil
}

// LoadTokens reads stored tokens from disk.
func LoadTokens() (*Tokens, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, TokenFile))
	if os.IsNotExist(err) {
		return &Tokens{}, nil
	} else if err != nil {
		return nil, fmt.Errorf("read tokens: %w", err)
	}
	var tok Tokens
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, fmt.Errorf("parse tokens: %w", err)
	}
	return &tok, nil
}

// SaveTokens persists tokens to disk.
func SaveTokens(t *Tokens) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	path := filepath.Join(dir, TokenFile)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write tokens: %w", err)
	}
	return nil
}
