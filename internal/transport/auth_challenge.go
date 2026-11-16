package transport

import (
	"context"
	"net/http"
)

type AuthChallenge struct {
	Status int
	Header http.Header
}
type challengeKey struct{}

func WithAuthChallenge(ctx context.Context, challenge *AuthChallenge) context.Context {
	return context.WithValue(ctx, challengeKey{}, challenge)
}

type challengeTransport struct{ http.RoundTripper }

func (t challengeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.RoundTripper.RoundTrip(request)
	if err == nil && (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden) {
		if challenge, ok := request.Context().Value(challengeKey{}).(*AuthChallenge); ok {
			challenge.Status, challenge.Header = response.StatusCode, response.Header.Clone()
		}
	}
	return response, err
}
