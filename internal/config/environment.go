package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

// EncryptionKey is a validated base64-encoded 32-byte database master key.
type EncryptionKey []byte

func (k *EncryptionKey) UnmarshalText(value []byte) error {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(value)))
	if err != nil || len(decoded) != 32 {
		return fmt.Errorf("expected a base64-encoded 32-byte key")
	}
	*k = decoded
	return nil
}

type Mode string

const (
	Primary        Mode = "primary"
	Peer           Mode = "peer"
	DefaultDataDir      = ".mcpproxy"
)

// Setting describes one environment input, declared on Config or a nested block.
type Setting struct {
	Name, Mode string
	Field      string
	Type       reflect.Type
	Secret     bool
}

// Settings enumerates the schema without reading the process environment.
func Settings() []Setting {
	var out []Setting
	var walk func(reflect.Type, string)
	walk = func(t reflect.Type, mode string) {
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			tag, ok := f.Tag.Lookup("env")
			if !ok || tag == "-" {
				continue
			}
			fieldMode := mode
			if m := f.Tag.Get("mode"); m != "" {
				fieldMode = m
			}
			name := strings.Split(tag, ",")[0]
			if name != "" {
				out = append(out, Setting{Name: name, Mode: fieldMode, Field: f.Name, Type: f.Type, Secret: f.Tag.Get("secret") == "true"})
				continue
			}
			walk(f.Type, fieldMode)
		}
	}
	walk(reflect.TypeOf(Config{}), string(Primary))
	return out
}

// Load reads deployment settings once. Only secret fields accept _FILE inputs.
func Load(mode Mode) (*Config, error) {
	return loadEnvironment(mode, env.ToMap(os.Environ()))
}

func loadEnvironment(mode Mode, input map[string]string) (*Config, error) {
	if mode != Primary && mode != Peer {
		return nil, fmt.Errorf("unknown mode %q", mode)
	}
	cfg := DefaultConfig()

	schema := Settings()
	known := make(map[string]Setting, len(schema)*2)
	values := make(map[string]string)
	for _, s := range schema {
		known[s.Name] = s
		if s.Secret {
			known[s.Name+"_FILE"] = s
		}
	}
	names := make([]string, 0, len(input))
	for name := range input {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !strings.HasPrefix(name, "MCPPROXY_") {
			continue
		}
		s, ok := known[name]
		if !ok {
			return nil, fmt.Errorf("unknown setting %s", name)
		}
		if input[name] == "" {
			continue
		}
		if s.Mode != "both" && s.Mode != string(mode) {
			return nil, fmt.Errorf("%s is only available in %s mode", name, s.Mode)
		}
	}
	for _, s := range schema {
		if s.Mode != "both" && s.Mode != string(mode) {
			continue
		}
		value := input[s.Name]
		path := ""
		if s.Secret {
			path = input[s.Name+"_FILE"]
		}
		if value != "" && path != "" {
			return nil, fmt.Errorf("set only one of %s or %s_FILE", s.Name, s.Name)
		}
		if path != "" {
			var err error
			value, err = readSecretFile(s.Name, path)
			if err != nil {
				return nil, err
			}
		}
		if value != "" {
			values[s.Name] = value
		}
	}
	if err := env.ParseWithOptions(cfg, env.Options{Environment: values, FuncMap: map[reflect.Type]env.ParserFunc{
		reflect.TypeOf(Duration(0)): func(value string) (interface{}, error) { d, err := time.ParseDuration(value); return Duration(d), err },
	}}); err != nil {
		var parseErr env.ParseError
		if errors.As(err, &parseErr) {
			if parseErr.Name == "EncryptionKey" {
				return nil, fmt.Errorf("invalid MCPPROXY_ENCRYPTION_KEY: expected a base64-encoded 32-byte key")
			}
			var candidates []string
			for _, setting := range schema {
				if setting.Field == parseErr.Name && values[setting.Name] != "" {
					candidates = append(candidates, setting.Name)
				}
			}
			return nil, fmt.Errorf("invalid %s: expected %s", strings.Join(candidates, ", "), parseErr.Type)
		}

		return nil, fmt.Errorf("invalid environment settings")
	}
	if cfg.DataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		directory := DefaultDataDir
		if mode == Peer {
			directory = ".mcpproxy-peer"
		}
		cfg.DataDir = filepath.Join(home, directory)
	}
	if _, _, err := net.SplitHostPort(cfg.Listen); err != nil {
		return nil, fmt.Errorf("MCPPROXY_LISTEN must be a host:port address")
	}
	if strings.TrimSpace(cfg.DataDir) == "" {
		return nil, fmt.Errorf("MCPPROXY_DATA_DIR must not be empty")
	}
	switch cfg.Logging.Level {
	case "trace", "debug", "info", "warn", "error":
	default:
		return nil, fmt.Errorf("invalid MCPPROXY_LOG_LEVEL")
	}
	if cfg.ToolResponseLimit < 0 {
		return nil, fmt.Errorf("MCPPROXY_TOOL_RESPONSE_LIMIT must be non-negative")
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	if mode == Primary {
		if err := cfg.OIDC.Validate(); err != nil {
			return nil, err
		}

		if len(cfg.EncryptionKey) == 0 {
			return nil, fmt.Errorf("MCPPROXY_ENCRYPTION_KEY or MCPPROXY_ENCRYPTION_KEY_FILE is required")
		}
	} else {
		remote, err := url.Parse(cfg.PeerPrimaryURL)
		if err != nil || remote.Host == "" || remote.User != nil || (remote.Path != "" && remote.Path != "/") || remote.RawQuery != "" || remote.Fragment != "" || (remote.Scheme != "https" && remote.Scheme != "http") {
			return nil, fmt.Errorf("MCPPROXY_PEER_PRIMARY_URL must be an HTTP(S) origin")
		}
		cfg.PeerToken = strings.TrimSpace(cfg.PeerToken)
		if cfg.PeerToken == "" {
			return nil, fmt.Errorf("MCPPROXY_PEER_TOKEN or MCPPROXY_PEER_TOKEN_FILE is required")
		}
	}
	return cfg, nil
}

func readSecretFile(name, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read %s_FILE: %w", name, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect %s_FILE: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s_FILE must be a regular file", name)
	}
	if info.Mode().Perm()&0004 != 0 {
		return "", fmt.Errorf("%s_FILE must not be world-readable", name)
	}
	data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil {
		return "", fmt.Errorf("read %s_FILE: %w", name, err)
	}
	if len(data) > 1<<20 {
		return "", fmt.Errorf("%s_FILE exceeds 1 MiB", name)
	}
	value := strings.TrimRight(string(data), "\r\n")
	if value == "" {
		return "", fmt.Errorf("%s_FILE is empty", name)
	}
	return value, nil
}
