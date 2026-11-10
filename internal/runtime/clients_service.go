package runtime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"github.com/google/uuid"

	"github.com/slopster67/mcpproxy-go/internal/auth"
	"github.com/slopster67/mcpproxy-go/internal/config"
	"github.com/slopster67/mcpproxy-go/internal/profile"
	"github.com/slopster67/mcpproxy-go/internal/reqcontext"
	"github.com/slopster67/mcpproxy-go/internal/storage"
)

// ClientsService manages saved clients, profile bindings and their single token.
// Mutations share the runtime binding mutex so identity and policy writes stay ordered.
type ClientsService struct {
	hostExists func(string) bool
	store      ClientCredentialStore
	hmacKey    func() ([]byte, error)
	cfg        func() *config.Config
	activity   func(*storage.ActivityRecord) error
	changed    func(clientID string)
	now        func() time.Time
	mu         *sync.Mutex
	logger     *zap.Logger

	notifier BindingNotifier
}

// ClientCredentialStore is the token-store surface the service needs;
// *storage.Manager implements it.
type ClientCredentialStore interface {
	MintClientCredentialWith(clientID, rawToken string, hmacKey []byte, opts storage.ClientMintOptions) (*auth.ClientCredential, error)
	RotateClientCredential(clientID, newRawToken string, hmacKey []byte) (*auth.ClientCredential, error)
	DeleteClientCredential(clientID string) (*auth.ClientCredential, error)
	UpdateClientCredentialBinding(clientID, pin, mode, host, displayName string, allowManaging *bool, allowCodeExecution *bool) (before, after *auth.ClientCredential, err error)
	ListClientCredentials() ([]auth.ClientCredential, error)
}

// BindingNotifier delivers the live effects for a token whose binding
// changed: clear the stored set_profile selection of every session it
// authenticated and send notifications/tools/list_changed to each. The server
// implements it; it is called synchronously so clearing is complete before
// the mutation returns.
type BindingNotifier interface {
	NotifyBindingChanged(tokenName string)
}

// Actor attributes a mutation on the profile_change record.
type Actor struct {
	Kind    string          // actor_kind: session, client_token or an internal operation
	Name    string          // actor_name: token name / user email, "" otherwise
	Surface profile.Surface // web, macos, cli, mcp, api
}

// ActorFromContext derives the actor from the request's AuthContext.
func ActorFromContext(ctx context.Context, surface profile.Surface) Actor {
	a := Actor{Surface: surface}
	if surface == "" {
		a.Surface = profile.SurfaceAPI
	}
	ac := auth.AuthContextFromContext(ctx)
	if ac == nil {
		a.Kind = string(auth.CredentialKindAnonymous)
		return a
	}
	a.Kind = string(ac.CredentialKind)
	if a.Kind == "" {
		// REST contexts do not stamp CredentialKind; infer it from the type so
		// the record still names who acted.
		switch {
		case ac.Type == auth.AuthTypeClient:
			a.Kind = string(auth.CredentialKindClientToken)
		case ac.IsAdmin():
			a.Kind = string(auth.CredentialKindSession)
		default:
			a.Kind = string(auth.CredentialKindAnonymous)
		}
	}
	switch {
	case ac.AgentName != "":
		a.Name = ac.AgentName
	case ac.Email != "":
		a.Name = ac.Email
	case ac.DisplayName != "":
		a.Name = ac.DisplayName
	case ac.UserID != "":
		a.Name = ac.UserID
	}
	return a
}

// ClientCredentialView is a client's credential as the surfaces show it.
type ClientCredentialView struct {
	AllowCodeExecution bool                    `json:"allow_code_execution"`
	AllowManaging      bool                    `json:"allow_managing"`
	HostID             string                  `json:"host_id,omitempty"`
	ID                 string                  `json:"id"`
	DisplayName        string                  `json:"display_name,omitempty"`
	TokenName          string                  `json:"token_name"`
	Profile            string                  `json:"profile"`
	Mode               string                  `json:"mode"`
	CredentialState    profile.CredentialState `json:"credential_state"`
	ConnectedAt        *time.Time              `json:"connected_at,omitempty"`
}

// ValidationError is a 400 {error, field} refusal.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

// NoClientCredentialError is the 409 no_client_credential refusal: the client
// has no active client credential, so a binding operation would have to mint
// one implicitly, which is forbidden.
type NoClientCredentialError struct {
	ClientID string
	State    profile.CredentialState
}

func (e *NoClientCredentialError) Error() string {
	return fmt.Sprintf("client %s has no active client credential; connect it with a profile first", e.ClientID)
}

// Code is the wire `code` of the refusal.
func (e *NoClientCredentialError) Code() string { return profile.ErrorCodeNoClientCredential }

// PreconditionFailedError is the 409 precondition_failed refusal.
type PreconditionFailedError struct{}

func (e *PreconditionFailedError) Error() string {
	return "the state changed since the preview; preview again"
}

// Code is the wire `code`.
func (e *PreconditionFailedError) Code() string { return profile.ErrorCodePreconditionFailed }

// ConnectInProgressError is the 409 connect_in_progress refusal: a connect of
// the client holds the in-flight claim, so another connect, rotate,
// finalize or binding change must wait for it.
type ConnectInProgressError struct{ ClientID string }

func (e *ConnectInProgressError) Error() string {
	return fmt.Sprintf("a connect of %s is already in progress; retry when it finishes", e.ClientID)
}

// Code is the wire `code` of the refusal.
func (e *ConnectInProgressError) Code() string { return profile.ErrorCodeConnectInProgress }

// CredentialSupersededError is the 409 credential_superseded refusal: the
// credential a connect wrote was replaced or revoked before it could be
// finalized, so the written secret must not be treated as live.
type CredentialSupersededError struct{ ClientID string }

func (e *CredentialSupersededError) Error() string {
	return fmt.Sprintf("the credential written for %s was replaced or revoked before it could be finalized; reconnect the client", e.ClientID)
}

// Code is the wire `code` of the refusal.
func (e *CredentialSupersededError) Code() string { return profile.ErrorCodeCredentialSuperseded }

// ClientsServiceDeps wires a ClientsService. Zero values are safe in tests:
// Now defaults to time.Now, Mu to a private mutex, Guard to the conservative
// evaluator, Logger to a no-op.
type ClientsServiceDeps struct {
	HostExists func(string) bool
	Store      ClientCredentialStore
	HMACKey    func() ([]byte, error)
	Config     func() *config.Config
	Activity   func(*storage.ActivityRecord) error
	// Changed is told the id of every client a write created, changed or
	// deleted (dashboard change stream).
	Changed func(clientID string)
	Now     func() time.Time
	Mu      *sync.Mutex
	Logger  *zap.Logger
}

// NewClientsService builds the service from its dependencies.
func NewClientsService(d ClientsServiceDeps) *ClientsService {
	s := &ClientsService{
		hostExists: d.HostExists, store: d.Store, hmacKey: d.HMACKey, cfg: d.Config,
		activity: d.Activity, changed: d.Changed, now: d.Now, mu: d.Mu, logger: d.Logger,
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.mu == nil {
		s.mu = &sync.Mutex{}
	}

	if s.logger == nil {
		s.logger = zap.NewNop()
	}
	if s.cfg == nil {
		s.cfg = func() *config.Config { return nil }
	}
	return s
}

func (s *ClientsService) emitChanged(clientID string) {
	if s.changed != nil {
		s.changed(clientID)
	}
}

// SetNotifier installs the live-session notifier (the server).
func (s *ClientsService) SetNotifier(n BindingNotifier) { s.notifier = n }

// --- helpers -------------------------------------------------------------

// Records returns every ownerless token record that concerns a client
// credential: the kind=client records (any state) and any regular token that
// holds a client-<id> name (a naming conflict). The REST clients list reads
// it once to decorate its rows.
func (s *ClientsService) Records() ([]auth.ClientCredential, error) {
	all, err := s.records()
	if err != nil {
		return nil, err
	}
	out := make([]auth.ClientCredential, 0, len(all))
	for i := range all {
		t := &all[i]
		if t.Kind == auth.KindClient || strings.HasPrefix(t.Name, "client-") {
			out = append(out, *t)
		}
	}
	return out, nil
}

// Now is the service's clock (tests inject one); REST uses it so "expiring"
// and "last observed" agree with the service.
func (s *ClientsService) Now() time.Time { return s.now() }

// StateOf classifies a credential record (client | revoked | expired | none).
func (s *ClientsService) StateOf(t *auth.ClientCredential) profile.CredentialState {
	return s.stateOf(t)
}

func (s *ClientsService) records() ([]auth.ClientCredential, error) {
	all, err := s.store.ListClientCredentials()
	if err != nil {
		return nil, fmt.Errorf("cannot read client credentials: %w", err)
	}
	return all, nil
}

// clientRecord finds the token named client-<id> among all (any kind).
func clientRecord(all []auth.ClientCredential, clientID string) *auth.ClientCredential {
	name := auth.ClientTokenName(clientID)
	for i := range all {
		if all[i].Name == name {
			return &all[i]
		}
	}
	return nil
}

// stateOf reports the credential state of a record.
func (s *ClientsService) stateOf(t *auth.ClientCredential) profile.CredentialState {
	switch {
	case t == nil || t.Kind != auth.KindClient:
		return profile.CredentialStateNone

	default:
		return profile.CredentialStateClient
	}
}

func (s *ClientsService) view(t *auth.ClientCredential) *ClientCredentialView {
	return &ClientCredentialView{AllowCodeExecution: t.AllowCodeExecution, AllowManaging: t.AllowManaging, HostID: t.HostID,
		ID: t.ClientID, DisplayName: t.DisplayName, TokenName: t.Name, Profile: t.ProfilePin, Mode: t.ProfileMode,
		CredentialState: s.stateOf(t), ConnectedAt: t.ConnectedAt,
	}
}

func (s *ClientsService) profileExists(cfg *config.Config, name string) bool {
	if cfg == nil {
		return false
	}
	for i := range cfg.Profiles {
		if cfg.Profiles[i].ID == name {
			return true
		}
	}
	return false
}

// resolveMode applies the binding-mode defaults and validation. modeArg nil = "not
// specified": keep the record's mode (a binding change), or the default for a
// fresh credential.
func resolveMode(profileName string, modeArg *string, keep string) (string, error) {
	if profileName == "" {
		return auth.ProfileModeSwitchable, nil
	}

	mode := keep
	switch {
	case modeArg != nil:
		mode = *modeArg
	case mode == "":
		mode = auth.ProfileModeLocked
	}
	if mode != auth.ProfileModeLocked && mode != auth.ProfileModeSwitchable {
		return "", &ValidationError{Field: "mode", Message: fmt.Sprintf("invalid mode %q: must be locked or switchable", mode)}
	}

	return mode, nil
}

// --- profile_change records ----------------------------------------------

type changeRecord struct {
	change          profile.ChangeKind
	profile         string
	previousProfile string
	clientID        string
	tokenName       string
	diff            map[string]interface{}
}

func surfaceSource(s profile.Surface) storage.ActivitySource {
	switch s {
	case profile.SurfaceMCP:
		return storage.ActivitySourceMCP
	default:
		return storage.ActivitySourceAPI
	}
}

func (s *ClientsService) writeChange(ctx context.Context, a Actor, c changeRecord) {
	writeChangeRecord(ctx, s.activity, s.logger, s.now(), a, c)
}

// writeChangeRecord builds and saves one `profile_change` activity record. It
// is shared by the clients service and the profiles/config funnel, so every
// record has one shape.
func writeChangeRecord(ctx context.Context, activity func(*storage.ActivityRecord) error, logger *zap.Logger, now time.Time, a Actor, c changeRecord) {
	if activity == nil {
		return
	}
	meta := map[string]interface{}{
		"actor_kind":       a.Kind,
		"actor_name":       a.Name,
		"surface":          string(a.Surface),
		"change":           string(c.change),
		"profile":          c.profile,
		"previous_profile": c.previousProfile,
		"client_id":        c.clientID,
		"token_name":       c.tokenName,
	}
	if len(c.diff) > 0 {
		meta["diff"] = c.diff
	}
	rec := &storage.ActivityRecord{
		Type:      storage.ActivityTypeProfileChange,
		Source:    surfaceSource(a.Surface),
		Status:    "success",
		Timestamp: now.UTC(),
		RequestID: reqcontext.GetRequestID(ctx),
		Metadata:  meta,
		// First-class beside the metadata keys so
		// /activity?client=&type=profile_change finds it.
		// Profile is the NEW profile; a profile_change is not a resolution, so
		// ProfileSource stays empty.
		Profile:   c.profile,
		ClientID:  c.clientID,
		TokenName: c.tokenName,
	}
	if err := activity(rec); err != nil {
		logger.Error("failed to write profile_change activity record", zap.String("client_id", c.clientID), zap.Error(err))
	}
}

func (s *ClientsService) announce(before, after *auth.ClientCredential) {
	s.emitChanged(after.ClientID)
	if s.notifier != nil {
		s.notifier.NotifyBindingChanged(after.Name)
	}
}

// bindingChange classifies a before/after pair into its single profile_change
// record (data-model §5): a profile change is `assign` (the mode change, if
// any, rides in diff); with the same profile a switchable→locked flip is
// `lock` and locked→switchable is `unlock`.
func bindingChange(before, after *auth.ClientCredential) (profile.ChangeKind, map[string]interface{}) {
	diff := map[string]interface{}{}
	if before.AllowCodeExecution != after.AllowCodeExecution {
		diff["allow_code_execution"] = map[string]interface{}{"from": before.AllowCodeExecution, "to": after.AllowCodeExecution}
	}
	if before.AllowManaging != after.AllowManaging {
		diff["allow_managing"] = map[string]interface{}{"from": before.AllowManaging, "to": after.AllowManaging}
	}
	if before.DisplayName != after.DisplayName {
		diff["display_name"] = map[string]interface{}{"from": before.DisplayName, "to": after.DisplayName}
	}
	if before.HostID != after.HostID {
		diff["host_id"] = map[string]interface{}{"from": before.HostID, "to": after.HostID}
	}
	if before.ProfileMode != after.ProfileMode {
		if diff == nil {
			diff = map[string]interface{}{}
		}
		diff["mode"] = map[string]interface{}{"from": before.ProfileMode, "to": after.ProfileMode}
	}
	if before.ProfilePin != after.ProfilePin {
		return profile.ChangeAssign, diff
	}
	if before.HostID != after.HostID && before.ProfileMode == after.ProfileMode {
		return profile.ChangeMoveHost, diff
	}
	if (before.AllowManaging != after.AllowManaging || before.AllowCodeExecution != after.AllowCodeExecution || before.DisplayName != after.DisplayName) && before.ProfileMode == after.ProfileMode {
		return profile.ChangeUpdate, diff
	}
	if after.ProfileMode == auth.ProfileModeLocked {
		return profile.ChangeLock, diff
	}
	return profile.ChangeUnlock, diff
}

// --- operations ------------------------------------------------------------

// SetBinding reassigns a client's profile and/or mode (assign|lock|unlock).
// It applies only to an active client credential, refuses a binding the
// binding guard would leave bypassable, clears the stored selection of
// every live session of the token, emits client.binding_changed, notifies the
// sessions, writes one profile_change record and never touches the client's
// config file. modeArg nil keeps the current mode, except profile "" which
// means All servers (switchable).
func (s *ClientsService) SetBinding(ctx context.Context, a Actor, clientID, profileName string, modeArg *string, hostArg *string, nameArg *string, allowManaging *bool, allowCodeExecution *bool) (*ClientCredentialView, error) {
	if !auth.ValidClientID(clientID) {
		return nil, &ValidationError{Field: "id", Message: fmt.Sprintf("invalid client id %q", clientID)}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.setBindingLocked(ctx, a, clientID, profileName, modeArg, hostArg, nameArg, allowManaging, allowCodeExecution)
}

func (s *ClientsService) setBindingLocked(ctx context.Context, a Actor, clientID, profileName string, modeArg *string, hostArg *string, nameArg *string, allowManaging *bool, allowCodeExecution *bool) (*ClientCredentialView, error) {
	all, err := s.records()
	if err != nil {
		return nil, err
	}
	rec := clientRecord(all, clientID)
	// The credential precondition (409) comes before request validation (400)
	// so a client without an active credential always gets no_client_credential.
	if state := s.stateOf(rec); state != profile.CredentialStateClient {
		return nil, &NoClientCredentialError{ClientID: clientID, State: state}
	}
	if profileName != "" && !s.profileExists(s.cfg(), profileName) {
		return nil, &ValidationError{Field: "profile", Message: fmt.Sprintf("unknown profile %q", profileName)}
	}
	keepMode := rec.ProfileMode
	if rec.ProfilePin == "" && profileName != "" && modeArg == nil {
		keepMode = ""
	}
	mode, err := resolveMode(profileName, modeArg, keepMode)
	if err != nil {
		return nil, err
	}
	host := rec.HostID
	if hostArg != nil {
		host = *hostArg
	}
	if host != "" && (s.hostExists == nil || !s.hostExists(host)) {
		return nil, &ValidationError{Field: "host_id", Message: "unknown connection host"}
	}
	displayName := rec.DisplayName
	if nameArg != nil {
		if hit := NewCredentialScreen().ScreenFields([]ScreenField{{Name: "display_name", Values: []string{*nameArg}}}); hit != nil {
			return nil, hit
		}
		displayName = strings.TrimSpace(*nameArg)
		if displayName == "" {
			return nil, &ValidationError{Field: "display_name", Message: "client name is required"}
		}
		if utf8.RuneCountInString(displayName) > auth.MaxClientDisplayName {
			return nil, &ValidationError{Field: "display_name", Message: "client name is too long"}
		}
	}
	if rec.HostID == host && rec.DisplayName == displayName && rec.ProfilePin == profileName && rec.ProfileMode == mode && (allowManaging == nil || rec.AllowManaging == *allowManaging) && (allowCodeExecution == nil || rec.AllowCodeExecution == *allowCodeExecution) {
		return s.view(rec), nil // no-op: no record, no notification
	}
	before, after, err := s.store.UpdateClientCredentialBinding(clientID, profileName, mode, host, displayName, allowManaging, allowCodeExecution)
	if err != nil {
		if errors.Is(err, storage.ErrClientCredentialNotFound) {
			return nil, &NoClientCredentialError{ClientID: clientID, State: profile.CredentialStateNone}
		}
		return nil, err
	}
	change, diff := bindingChange(before, after)
	s.writeChange(ctx, a, changeRecord{
		change: change, profile: after.ProfilePin, previousProfile: before.ProfilePin,
		clientID: clientID, tokenName: after.Name, diff: diff,
	})
	s.announce(before, after)
	return s.view(after), nil
}

// Skipped is a bulk-move client that was not moved.
type Skipped struct {
	ClientID string `json:"client_id"`
	Code     string `json:"code"`
	Error    string `json:"error"`
}

// BulkAssign moves every active client credential bound to profile `from` to
// profile `to` (mode as in SetBinding). The guard and the credential
// precondition apply per client: refused clients are reported in skipped[]
// with the same code the single operation returns, the others are moved and
// each writes its own record.
func (s *ClientsService) BulkAssign(ctx context.Context, a Actor, from, to string, modeArg *string) (moved []string, skipped []Skipped, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.records()
	if err != nil {
		return nil, nil, err
	}
	var ids []string
	for i := range all {
		t := &all[i]
		if t.Kind == auth.KindClient && t.ProfilePin == from {
			ids = append(ids, t.ClientID)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {

		if _, err := s.setBindingLocked(ctx, a, id, to, modeArg, nil, nil, nil, nil); err != nil {
			skipped = append(skipped, skipReason(id, err))
			continue
		}
		moved = append(moved, id)
	}
	return moved, skipped, nil
}

func skipReason(id string, err error) Skipped {
	var noCred *NoClientCredentialError
	var busy *ConnectInProgressError
	var val *ValidationError
	switch {
	case errors.As(err, &busy):
		return Skipped{ClientID: id, Code: busy.Code(), Error: err.Error()}

	case errors.As(err, &noCred):
		return Skipped{ClientID: id, Code: profile.ErrorCodeNoClientCredential, Error: err.Error()}
	case errors.As(err, &val):
		return Skipped{ClientID: id, Code: "invalid_" + val.Field, Error: err.Error()}
	default:
		return Skipped{ClientID: id, Code: "error", Error: err.Error()}
	}
}

// List returns every client credential (any state), sorted by client id.
func (s *ClientsService) List() ([]ClientCredentialView, error) {
	all, err := s.records()
	if err != nil {
		return nil, err
	}
	var out []ClientCredentialView
	for i := range all {
		if all[i].Kind == auth.KindClient {
			out = append(out, *s.view(&all[i]))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Get returns one client's credential view, or nil when it has none.
func (s *ClientsService) Get(clientID string) (*ClientCredentialView, error) {
	all, err := s.records()
	if err != nil {
		return nil, err
	}
	rec := clientRecord(all, clientID)
	if rec == nil || rec.Kind != auth.KindClient {
		return nil, nil
	}
	return s.view(rec), nil
}

// Delete permanently removes a client's credential record and frees its name.
func (s *ClientsService) Delete(ctx context.Context, a Actor, clientID string) error {
	if !auth.ValidClientID(clientID) {
		return &ValidationError{Field: "id", Message: fmt.Sprintf("invalid client id %q", clientID)}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, err := s.store.DeleteClientCredential(clientID)
	if errors.Is(err, storage.ErrClientCredentialNotFound) {
		return &NoClientCredentialError{ClientID: clientID, State: profile.CredentialStateNone}
	}
	if err != nil {
		return err
	}
	s.writeChange(ctx, a, changeRecord{change: profile.ChangeDelete, profile: rec.ProfilePin, previousProfile: rec.ProfilePin, clientID: clientID, tokenName: rec.Name})
	s.emitChanged(clientID)
	return nil
}

// Rotate replaces the current token immediately and returns the new secret once.
func (s *ClientsService) Rotate(ctx context.Context, a Actor, clientID string) (secret string, view *ClientCredentialView, err error) {
	if !auth.ValidClientID(clientID) {
		return "", nil, &ValidationError{Field: "id", Message: fmt.Sprintf("invalid client id %q", clientID)}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	key, err := s.hmacKey()
	if err != nil {
		return "", nil, err
	}
	raw, err := auth.GenerateClientToken()
	if err != nil {
		return "", nil, err
	}
	updated, err := s.store.RotateClientCredential(clientID, raw, key)
	if err != nil {
		if errors.Is(err, storage.ErrClientCredentialNotFound) {
			return "", nil, &NoClientCredentialError{ClientID: clientID, State: profile.CredentialStateNone}
		}
		return "", nil, err
	}
	s.writeChange(ctx, a, changeRecord{change: profile.ChangeRotate, profile: updated.ProfilePin, previousProfile: updated.ProfilePin, clientID: clientID, tokenName: updated.Name})
	s.emitChanged(clientID)
	return raw, s.view(updated), nil
}

// AddRequest contains the user-supplied client name and binding settings.
type AddRequest struct {
	AllowCodeExecution bool `json:"allow_code_execution"`
	AllowManaging      bool
	HostID             string
	DisplayName        string
	Profile            string
	Mode               *string
}

// Add creates a saved client and returns its credential once.
func (s *ClientsService) Add(ctx context.Context, a Actor, req AddRequest) (*ClientCredentialView, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.HostID != "" && (s.hostExists == nil || !s.hostExists(req.HostID)) {
		return nil, "", &ValidationError{Field: "host_id", Message: "unknown execution host"}
	}
	modeText := ""
	if req.Mode != nil {
		modeText = *req.Mode
	}
	if hit := NewCredentialScreen().ScreenFields([]ScreenField{
		{Name: "display_name", Values: []string{req.DisplayName}},
		{Name: "profile", Values: []string{req.Profile}}, {Name: "mode", Values: []string{modeText}},
	}); hit != nil {
		return nil, "", hit
	}
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if req.DisplayName == "" {
		return nil, "", &ValidationError{Field: "display_name", Message: "client name is required"}
	}
	if utf8.RuneCountInString(req.DisplayName) > auth.MaxClientDisplayName {
		return nil, "", &ValidationError{Field: "display_name", Message: "client name is too long"}
	}
	if req.Profile != "" && !s.profileExists(s.cfg(), req.Profile) {
		return nil, "", &ValidationError{Field: "profile", Message: "unknown profile"}
	}
	mode, err := resolveMode(req.Profile, req.Mode, "")
	if err != nil {
		return nil, "", err
	}
	key, err := s.hmacKey()
	if err != nil {
		return nil, "", err
	}
	raw, err := auth.GenerateClientToken()
	if err != nil {
		return nil, "", err
	}
	identity, err := uuid.NewRandom()
	if err != nil {
		return nil, "", fmt.Errorf("generate client ID: %w", err)
	}
	clientID := "client-" + identity.String()
	rec, err := s.store.MintClientCredentialWith(clientID, raw, key, storage.ClientMintOptions{AllowCodeExecution: req.AllowCodeExecution, AllowManaging: req.AllowManaging, HostID: req.HostID, Mode: mode, Pin: req.Profile, DisplayName: req.DisplayName, Issuer: &auth.CredentialIssuer{ActorKind: a.Kind, ActorName: a.Name, Surface: string(a.Surface)}})
	if err != nil {
		return nil, "", err
	}
	s.writeChange(ctx, a, changeRecord{change: profile.ChangeIssue, profile: rec.ProfilePin, clientID: clientID, tokenName: rec.Name})
	s.emitChanged(clientID)
	return s.view(rec), raw, nil
}
