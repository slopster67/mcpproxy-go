package secret

import (
	"context"
	"fmt"
)

// Store is the primary's encrypted database. Peers never construct this provider.
type Store interface {
	GetSecret(string) (string, error)
	SetSecret(string, string) error
	DeleteSecret(string) error
	ListSecrets() ([]string, error)
}
type StoreProvider struct{ store Store }

func (p *StoreProvider) CanResolve(kind string) bool { return kind == "secret" }
func (p *StoreProvider) IsAvailable() bool           { return p.store != nil }
func (p *StoreProvider) Resolve(ctx context.Context, ref Ref) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if p.store == nil {
		return "", fmt.Errorf("primary secret store unavailable")
	}
	return p.store.GetSecret(ref.Name)
}
func (p *StoreProvider) Store(ctx context.Context, ref Ref, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.store == nil {
		return fmt.Errorf("primary secret store unavailable")
	}
	return p.store.SetSecret(ref.Name, value)
}
func (p *StoreProvider) Delete(ctx context.Context, ref Ref) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.store == nil {
		return fmt.Errorf("primary secret store unavailable")
	}
	return p.store.DeleteSecret(ref.Name)
}
func (p *StoreProvider) List(ctx context.Context) ([]Ref, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.store == nil {
		return nil, fmt.Errorf("primary secret store unavailable")
	}
	names, err := p.store.ListSecrets()
	if err != nil {
		return nil, err
	}
	refs := make([]Ref, 0, len(names))
	for _, name := range names {
		refs = append(refs, Ref{Type: "secret", Name: name, Original: "${secret:" + name + "}"})
	}
	return refs, nil
}
