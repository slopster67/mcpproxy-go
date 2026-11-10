package diagnostics

// WrapError attaches an explicit diagnostic code to err, which Classify then
// returns without inspecting the error text.
func WrapError(code Code, err error) error {
	if err == nil {
		return nil
	}
	return &codedError{code: code, inner: err}
}

// codedError is the concrete implementation of the errors.Is-friendly
// "has-code" interface the classifier's fast path looks for.
type codedError struct {
	code  Code
	inner error
}

func (e *codedError) Error() string {
	return e.inner.Error()
}

func (e *codedError) Unwrap() error {
	return e.inner
}

// Code returns the attached diagnostic code. Matches the interface used by
// Classify's fast path (`interface{ Code() Code }`).
func (e *codedError) Code() Code {
	return e.code
}

// Convenience shortcuts — thin wrappers around WrapError so call-sites in
// other packages don't have to import the stable code constants just to
// attach them.

// WrapOAuthRefreshExpired marks an error as the refresh-token-expired code.
func WrapOAuthRefreshExpired(err error) error { return WrapError(OAuthRefreshExpired, err) }

// WrapOAuthRefresh403 marks an error as an OAuth refresh 403 / invalid_grant.
func WrapOAuthRefresh403(err error) error { return WrapError(OAuthRefresh403, err) }
