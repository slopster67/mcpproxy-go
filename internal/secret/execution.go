package secret

import (
	"context"
	"fmt"
	"reflect"
)

func (r *Resolver) References(value any) []Ref {
	refs := []Ref{}
	r.extractSecretRefs(reflect.ValueOf(value), "", &refs)
	return refs
}

// ExecutionResolver resolves environment references locally and secret store names
// exclusively from the primary's per-server credential snapshot.
func ExecutionResolver(values map[string]string) *Resolver {
	return &Resolver{providers: map[string]Provider{"env": NewEnvProvider(), "secret": &suppliedSecrets{values: values}}}
}

type suppliedSecrets struct{ values map[string]string }

func (p *suppliedSecrets) CanResolve(kind string) bool { return kind == "secret" }
func (p *suppliedSecrets) IsAvailable() bool           { return true }
func (p *suppliedSecrets) Resolve(_ context.Context, ref Ref) (string, error) {
	value, ok := p.values[ref.Name]
	if !ok {
		return "", fmt.Errorf("secret %q is missing on the primary", ref.Name)
	}
	return value, nil
}
func (p *suppliedSecrets) Store(context.Context, Ref, string) error {
	return fmt.Errorf("secrets are managed on the primary")
}
func (p *suppliedSecrets) Delete(context.Context, Ref) error {
	return fmt.Errorf("secrets are managed on the primary")
}
func (p *suppliedSecrets) List(context.Context) ([]Ref, error) {
	refs := []Ref{}
	for name := range p.values {
		refs = append(refs, Ref{Type: "secret", Name: name})
	}
	return refs, nil
}
