package secret

import (
	"context"
	"fmt"
	"reflect"
)

// NewResolver creates a new secret resolver
func NewResolver(store Store) *Resolver {
	r := &Resolver{
		providers: make(map[string]Provider),
	}

	// Register default providers
	r.RegisterProvider("env", NewEnvProvider())
	r.RegisterProvider("secret", &StoreProvider{store: store})

	return r
}

// RegisterProvider registers a new secret provider
func (r *Resolver) RegisterProvider(secretType string, provider Provider) {
	r.providers[secretType] = provider
}

// Resolve resolves a single secret reference
func (r *Resolver) Resolve(ctx context.Context, ref Ref) (string, error) {
	provider, exists := r.providers[ref.Type]
	if !exists {
		return "", fmt.Errorf("no provider for secret type: %s", ref.Type)
	}

	if !provider.CanResolve(ref.Type) {
		return "", fmt.Errorf("provider cannot resolve secret type: %s", ref.Type)
	}

	if !provider.IsAvailable() {
		return "", fmt.Errorf("provider for %s is not available on this system", ref.Type)
	}

	return provider.Resolve(ctx, ref)
}

// Store stores a secret using the appropriate provider
func (r *Resolver) Store(ctx context.Context, ref Ref, value string) error {
	provider, exists := r.providers[ref.Type]
	if !exists {
		return fmt.Errorf("no provider for secret type: %s", ref.Type)
	}

	if !provider.IsAvailable() {
		return fmt.Errorf("provider for %s is not available on this system", ref.Type)
	}

	return provider.Store(ctx, ref, value)
}

// Delete deletes a secret using the appropriate provider
func (r *Resolver) Delete(ctx context.Context, ref Ref) error {
	provider, exists := r.providers[ref.Type]
	if !exists {
		return fmt.Errorf("no provider for secret type: %s", ref.Type)
	}

	if !provider.IsAvailable() {
		return fmt.Errorf("provider for %s is not available on this system", ref.Type)
	}

	return provider.Delete(ctx, ref)
}

// ListAll lists all secret references from all providers
func (r *Resolver) ListAll(ctx context.Context) ([]Ref, error) {
	var allRefs []Ref

	for _, provider := range r.providers {
		if !provider.IsAvailable() {
			continue
		}

		refs, err := provider.List(ctx)
		if err != nil {
			// Log error but continue with other providers
			continue
		}

		allRefs = append(allRefs, refs...)
	}

	return allRefs, nil
}

// SecretExpansionError records a failure to resolve a single secret reference during struct expansion.
type SecretExpansionError struct {
	FieldPath string // e.g. "WorkingDir", "Isolation.WorkingDir", "Args[0]", "Env[MY_VAR]"
	Reference string // the original unresolved reference pattern, e.g. "${env:HOME}"
	Err       error
}

// ExpandStructSecretsCollectErrors expands secret references in all string fields of v.
// It does not fail fast: it collects all expansion errors and
// continues processing remaining fields. On error, the field retains its original value.
// v must be a non-nil pointer to a struct.
func (r *Resolver) ExpandStructSecretsCollectErrors(ctx context.Context, v interface{}) []SecretExpansionError {
	var errs []SecretExpansionError
	r.expandValueCollectErrors(ctx, reflect.ValueOf(v), "", &errs)
	return errs
}

// expandValueCollectErrors mirrors expandValue but tracks field paths and collects errors
// instead of returning on the first failure. On resolution error the field is left unchanged.
func (r *Resolver) expandValueCollectErrors(ctx context.Context, v reflect.Value, path string, errs *[]SecretExpansionError) {
	if !v.IsValid() {
		return
	}

	// Handle pointers
	if v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return
		}
		r.expandValueCollectErrors(ctx, v.Elem(), path, errs)
		return
	}

	switch v.Kind() {
	case reflect.String:
		if v.CanSet() {
			original := v.String()
			if IsSecretRef(original) {
				expanded, err := r.ExpandSecretRefs(ctx, original)
				if err != nil {
					*errs = append(*errs, SecretExpansionError{
						FieldPath: path,
						Reference: original,
						Err:       err,
					})
					// retain original value on failure — do not call SetString
				} else {
					v.SetString(expanded)
				}
			}
		}

	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			field := v.Field(i)
			if !field.CanInterface() {
				continue
			}
			fieldType := t.Field(i)
			if !fieldType.IsExported() {
				continue
			}
			fieldName := fieldType.Name
			newPath := fieldName
			if path != "" {
				newPath = path + "." + fieldName
			}
			r.expandValueCollectErrors(ctx, field, newPath, errs)
		}

	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			newPath := fmt.Sprintf("%s[%d]", path, i)
			r.expandValueCollectErrors(ctx, v.Index(i), newPath, errs)
		}

	case reflect.Map:
		for _, key := range v.MapKeys() {
			keyStr := fmt.Sprintf("%v", key.Interface())
			newPath := fmt.Sprintf("%s[%s]", path, keyStr)
			mapValue := v.MapIndex(key)
			if mapValue.Kind() == reflect.String && IsSecretRef(mapValue.String()) {
				original := mapValue.String()
				expanded, err := r.ExpandSecretRefs(ctx, original)
				if err != nil {
					*errs = append(*errs, SecretExpansionError{
						FieldPath: newPath,
						Reference: original,
						Err:       err,
					})
				} else {
					v.SetMapIndex(key, reflect.ValueOf(expanded))
				}
			} else if mapValue.Kind() == reflect.Interface {
				actualValue := mapValue.Elem()
				if actualValue.Kind() == reflect.String && IsSecretRef(actualValue.String()) {
					original := actualValue.String()
					expanded, err := r.ExpandSecretRefs(ctx, original)
					if err != nil {
						*errs = append(*errs, SecretExpansionError{
							FieldPath: newPath,
							Reference: original,
							Err:       err,
						})
					} else {
						v.SetMapIndex(key, reflect.ValueOf(expanded))
					}
				}
			}
		}
	}
}

func (r *Resolver) InspectReferences(ctx context.Context, allRefs []Ref) (*ConfigSecretsResponse, error) {
	seen := map[string]bool{}
	// Separate secrets by type
	var secretStatus []SecretStatus
	var envRefs []Ref
	var envStatus []EnvVarStatus

	for _, ref := range allRefs {
		key := ref.Type + ":" + ref.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		switch ref.Type {
		case "secret":
			// Check if secret can be resolved
			provider, exists := r.providers["secret"]
			isSet := false
			if exists && provider.IsAvailable() {
				_, err := provider.Resolve(ctx, ref)
				isSet = err == nil
			}

			secretStatus = append(secretStatus, SecretStatus{
				Ref:   ref,
				IsSet: isSet,
			})
		case "env":
			envRefs = append(envRefs, ref)

			// Check if environment variable exists
			provider, exists := r.providers["env"]
			isSet := false
			if exists && provider.IsAvailable() {
				_, err := provider.Resolve(ctx, ref)
				isSet = err == nil
			}

			envStatus = append(envStatus, EnvVarStatus{Available: true,
				Ref:   ref,
				IsSet: isSet,
			})
		}
	}

	return &ConfigSecretsResponse{
		Secrets:         secretStatus,
		EnvironmentVars: envStatus,
		TotalSecrets:    len(secretStatus),
		TotalEnvVars:    len(envRefs),
	}, nil
}

// extractSecretRefs recursively extracts secret references from a struct
func (r *Resolver) extractSecretRefs(v reflect.Value, path string, refs *[]Ref) {
	if !v.IsValid() {
		return
	}

	// Handle pointers
	if v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return
		}
		r.extractSecretRefs(v.Elem(), path, refs)
		return
	}

	switch v.Kind() {
	case reflect.String:
		value := v.String()
		for _, ref := range FindSecretRefs(value) {
			*refs = append(*refs, *ref)
		}

	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			field := v.Field(i)
			if !field.CanInterface() {
				continue
			}

			fieldType := t.Field(i)
			fieldName := fieldType.Name

			// Skip unexported fields
			if !fieldType.IsExported() {
				continue
			}

			newPath := fieldName
			if path != "" {
				newPath = path + "." + fieldName
			}

			r.extractSecretRefs(field, newPath, refs)
		}

	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			newPath := fmt.Sprintf("%s[%d]", path, i)
			r.extractSecretRefs(v.Index(i), newPath, refs)
		}

	case reflect.Map:
		for _, key := range v.MapKeys() {
			keyStr := fmt.Sprintf("%v", key.Interface())
			newPath := fmt.Sprintf("%s[%s]", path, keyStr)
			r.extractSecretRefs(v.MapIndex(key), newPath, refs)
		}
	}
}
