package config

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// OAuthPatch contains only operator-supplied fields; null resets one field.
type OAuthPatch map[string]json.RawMessage

// PatchOAuth merges supplied fields with the current overrides. Null removes an
// override; an empty result restores automatic OAuth detection entirely.
func PatchOAuth(current *OAuthConfig, patch OAuthPatch) (*OAuthConfig, error) {
	// Validate the supplied field names even when their value is null.
	supplied, err := json.Marshal(patch)
	if err != nil {
		return nil, err
	}
	check := json.NewDecoder(bytes.NewReader(supplied))
	check.DisallowUnknownFields()
	if err := check.Decode(&OAuthConfig{}); err != nil {
		return nil, fmt.Errorf("invalid OAuth override: %w", err)
	}
	values := map[string]json.RawMessage{}
	if current != nil {
		data, _ := json.Marshal(current)
		_ = json.Unmarshal(data, &values)
	}
	for key, value := range patch {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			delete(values, key)
		} else {
			values[key] = value
		}
	}
	data, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	var result OAuthConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("invalid OAuth override: %w", err)
	}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	normalized, _ := json.Marshal(result)
	if bytes.Equal(normalized, []byte("{}")) {
		return nil, nil
	}
	return &result, nil
}
