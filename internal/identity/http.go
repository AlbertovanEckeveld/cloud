package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/AlbertovanEckeveld/cloud/internal/core/httpserver"
)

// Principal is the authenticated user of a request.
type Principal struct {
	User    User
	Session Session
}

type principalKey struct{}

// PrincipalFrom returns the authenticated user set by RequireUser.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

func (s *Service) cookieName() string {
	if s.secureCookies() {
		// The __Host- prefix makes browsers enforce Secure, Path=/ and no
		// Domain, so a sibling subdomain cannot set or overwrite it.
		return "__Host-session"
	}
	return "session"
}

func (s *Service) secureCookies() bool { return s.opts.PublicURL.Scheme == "https" }

func (s *Service) setSessionCookie(w http.ResponseWriter, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName(),
		Value:    token,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true, // not readable from JavaScript, so XSS cannot steal it
		Secure:   s.secureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Service) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName(), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode,
	})
}

// sessionFrom resolves the session cookie of a request.
func (s *Service) sessionFrom(r *http.Request) (Session, User, error) {
	c, err := r.Cookie(s.cookieName())
	if err != nil {
		return Session{}, User{}, ErrNoSession
	}
	return s.Authenticate(r.Context(), c.Value)
}

func clientFrom(r *http.Request) Client {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	ua := r.UserAgent()
	if len(ua) > 256 {
		ua = ua[:256]
	}
	return Client{IP: ip, UserAgent: ua}
}

// RequireUser rejects requests without a fully authenticated session and
// makes the Principal available through PrincipalFrom.
func (s *Service) RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, u, err := s.sessionFrom(r)
		if err != nil {
			s.writeError(w, r, err) // invalid session: also clears the cookie
			return
		}
		if sess.State != StateAuthenticated {
			// A login in progress: refuse, but keep the cookie so the user
			// can still complete the second factor.
			httpserver.WriteError(w, http.StatusUnauthorized, "unauthenticated", "Rond eerst het inloggen af.")
			return
		}
		ctx := context.WithValue(r.Context(), principalKey{}, Principal{User: u, Session: sess})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRecentAuth allows a request only shortly after the user signed in;
// for sensitive changes made by other modules. It must run after RequireUser.
func (s *Service) RequireRecentAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFrom(r.Context())
		if !ok {
			s.writeError(w, r, ErrNoSession)
			return
		}
		if err := s.requireRecentAuth(p.Session); err != nil {
			s.writeError(w, r, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// AuditRequest records an event of another module in the audit log, with the
// signed-in user as actor. It runs after the change committed (modules do
// not share transactions), so a failure is logged rather than undoing it.
func (s *Service) AuditRequest(r *http.Request, event string, details map[string]any) {
	p, ok := PrincipalFrom(r.Context())
	if !ok {
		return
	}
	s.auditBestEffort(r.Context(), clientFrom(r), auditEntry{event: event, actor: &p.User.ID, details: details})
}

// RequireAdmin must run after RequireUser.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := PrincipalFrom(r.Context()); !ok || !p.User.IsAdmin {
			httpserver.WriteError(w, http.StatusForbidden, "forbidden", "Je hebt geen toegang tot deze pagina.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Routes mounts the identity API: /auth/* for signing in, /account/* for
// the signed-in user's own account and /admin/* for administrators.
func (s *Service) Routes(r chi.Router) {
	r.Route("/auth", func(r chi.Router) {
		r.Get("/session", s.handleSession)
		r.Post("/login", s.handleLogin)
		r.Post("/login/totp", s.withSession(s.handleLoginTOTP))
		r.Post("/login/recovery", s.withSession(s.handleLoginRecovery))
		r.Post("/passkey/begin", s.handlePasskeyLoginBegin)
		r.Post("/passkey/finish", s.handlePasskeyLoginFinish)
		r.Post("/activate", s.handleActivate)
		r.Post("/totp/enrollment", s.withSession(s.handleBeginEnrollment))
		r.Post("/totp/enrollment/confirm", s.withSession(s.handleConfirmEnrollment))
		r.Post("/logout", s.withSession(s.handleLogout))
		r.Post("/logout-everywhere", s.withSession(func(w http.ResponseWriter, r *http.Request, sess Session, u User) {
			if sess.State != StateAuthenticated {
				s.writeError(w, r, ErrWrongState)
				return
			}
			s.handleLogoutEverywhere(w, r, sess, u)
		}))
	})
	r.Route("/account", func(r chi.Router) {
		r.Use(s.RequireUser)
		r.Get("/activity", s.handleActivity)
		r.Post("/password", s.handleChangePassword)
		r.Get("/passkeys", s.handleListPasskeys)
		r.Post("/passkeys/registration", s.handlePasskeyRegistrationBegin)
		r.Post("/passkeys/registration/finish", s.handlePasskeyRegistrationFinish)
		r.Delete("/passkeys/{id}", s.handleDeletePasskey)
		r.Get("/recovery-codes", s.handleRecoveryCodeStatus)
		r.Post("/recovery-codes", s.handleGenerateRecoveryCodes)
		r.Get("/sessions", s.handleListSessions)
		r.Delete("/sessions/{id}", s.handleRevokeSession)
		r.Post("/sessions/revoke-others", s.handleRevokeOtherSessions)
		r.Get("/groups", s.handleOwnGroups)
		r.Get("/apps", s.handleAppGrants)
		r.Delete("/apps/{clientID}", s.handleRevokeApp)
		r.Get("/profile", s.handleGetProfile)
		r.Patch("/profile", s.handleUpdateProfile)
		r.Put("/avatar", s.handleSetAvatar)
		r.Delete("/avatar", s.handleDeleteAvatar)
	})
	r.With(s.RequireUser).Get("/directory", s.handleDirectory)
	r.Route("/admin", s.adminRoutes)
	r.Route("/oidc", s.oidcRoutes)
}

type sessionHandler func(w http.ResponseWriter, r *http.Request, sess Session, u User)

// withSession resolves the session (full or partial); the handler checks the state.
func (s *Service) withSession(h sessionHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, u, err := s.sessionFrom(r)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		h(w, r, sess, u)
	}
}

type sessionResponse struct {
	State SessionState `json:"state"`
	User  *User        `json:"user,omitempty"`
}

func (s *Service) handleSession(w http.ResponseWriter, r *http.Request) {
	sess, u, err := s.sessionFrom(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	resp := sessionResponse{State: sess.State}
	// User details are only revealed once both factors are verified.
	if sess.State == StateAuthenticated {
		resp.User = &u
	}
	httpserver.WriteJSON(w, http.StatusOK, resp)
}

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	token, err := s.Login(r.Context(), clientFrom(r), in.Username, in.Password)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.setSessionCookie(w, token, partialSessionTTL)
	httpserver.WriteJSON(w, http.StatusOK, sessionResponse{State: StateMFARequired})
}

type codeRequest struct {
	Code string `json:"code"`
}

func (s *Service) handleLoginTOTP(w http.ResponseWriter, r *http.Request, sess Session, _ User) {
	var in codeRequest
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	token, err := s.VerifyTOTP(r.Context(), clientFrom(r), sess, in.Code)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.setSessionCookie(w, token, sessionMaxTTL)
	httpserver.WriteJSON(w, http.StatusOK, sessionResponse{State: StateAuthenticated})
}

func (s *Service) handleActivate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	token, err := s.Activate(r.Context(), clientFrom(r), in.Token, in.Password)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.setSessionCookie(w, token, partialSessionTTL)
	httpserver.WriteJSON(w, http.StatusOK, sessionResponse{State: StateMFAEnrollmentRequired})
}

func (s *Service) handleBeginEnrollment(w http.ResponseWriter, r *http.Request, sess Session, u User) {
	enrollment, err := s.BeginTOTPEnrollment(r.Context(), sess, u.Username)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, enrollment)
}

func (s *Service) handleConfirmEnrollment(w http.ResponseWriter, r *http.Request, sess Session, _ User) {
	var in codeRequest
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	token, err := s.ConfirmTOTPEnrollment(r.Context(), clientFrom(r), sess, in.Code)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.setSessionCookie(w, token, sessionMaxTTL)
	httpserver.WriteJSON(w, http.StatusOK, sessionResponse{State: StateAuthenticated})
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request, sess Session, _ User) {
	if err := s.Logout(r.Context(), clientFrom(r), sess); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleActivity(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	events, err := s.Activity(r.Context(), p.User.ID, 20)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Events []AuditEvent `json:"events"`
	}{events})
}

func (s *Service) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	p, _ := PrincipalFrom(r.Context())
	if err := s.ChangePassword(r.Context(), clientFrom(r), p.Session, in.CurrentPassword, in.NewPassword); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeError maps service errors to responses. Unknown errors are logged
// with the request ID and reported generically.
func (s *Service) writeError(w http.ResponseWriter, r *http.Request, err error) {
	if throttled, ok := errors.AsType[*ThrottledError](err); ok {
		minutes := int(math.Ceil(throttled.RetryAfter.Minutes()))
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(throttled.RetryAfter.Seconds()))))
		httpserver.WriteError(w, http.StatusTooManyRequests, "throttled",
			fmt.Sprintf("Te veel mislukte pogingen. Probeer het over %d minuten opnieuw.", max(minutes, 1)))
		return
	}
	if policy, ok := errors.AsType[*PolicyError](err); ok {
		httpserver.WriteError(w, http.StatusUnprocessableEntity, "weak_password", policy.Reason)
		return
	}
	if input, ok := errors.AsType[*InputError](err); ok {
		httpserver.WriteError(w, http.StatusUnprocessableEntity, "invalid_input", input.Reason)
		return
	}
	switch {
	case errors.Is(err, ErrNoSession):
		s.clearSessionCookie(w)
		httpserver.WriteError(w, http.StatusUnauthorized, "unauthenticated", "Je bent niet (meer) ingelogd.")
	case errors.Is(err, ErrInvalidCredentials):
		httpserver.WriteError(w, http.StatusUnauthorized, "invalid_credentials", "Gebruikersnaam of wachtwoord onjuist.")
	case errors.Is(err, ErrNotActivated):
		httpserver.WriteError(w, http.StatusForbidden, "not_activated", "Dit account is nog niet geactiveerd. Gebruik je activatielink.")
	case errors.Is(err, ErrInvalidCode):
		httpserver.WriteError(w, http.StatusUnauthorized, "invalid_code", "Deze code klopt niet. Controleer de tijd op je telefoon.")
	case errors.Is(err, ErrInvalidToken):
		httpserver.WriteError(w, http.StatusBadRequest, "invalid_token", "Deze activatielink is ongeldig of verlopen. Vraag een nieuwe aan.")
	case errors.Is(err, ErrReauthRequired):
		httpserver.WriteError(w, http.StatusForbidden, "reauth_required", "Log voor de veiligheid opnieuw in om dit te wijzigen.")
	case errors.Is(err, ErrNotFound):
		httpserver.WriteError(w, http.StatusNotFound, "not_found", "Niet gevonden.")
	case errors.Is(err, ErrPasskeyRejected):
		httpserver.WriteError(w, http.StatusBadRequest, "passkey_rejected", "De passkey kon niet worden geverifieerd.")
	case errors.Is(err, ErrChallengeInvalid):
		httpserver.WriteError(w, http.StatusBadRequest, "challenge_expired", "Dit verzoek is verlopen. Probeer het opnieuw.")
	case errors.Is(err, ErrLastAdmin):
		httpserver.WriteError(w, http.StatusConflict, "last_admin", "Er moet minstens één actieve beheerder overblijven.")
	case errors.Is(err, ErrSelf):
		httpserver.WriteError(w, http.StatusConflict, "self", "Dit kun je niet bij je eigen account doen.")
	case errors.Is(err, ErrUserDisabled):
		httpserver.WriteError(w, http.StatusConflict, "user_disabled", "Dit account is uitgeschakeld. Schakel het eerst in.")
	case errors.Is(err, ErrUserExists):
		httpserver.WriteError(w, http.StatusConflict, "username_taken", "Deze gebruikersnaam is al in gebruik.")
	case errors.Is(err, ErrEmailExists):
		httpserver.WriteError(w, http.StatusConflict, "email_taken", "Dit e-mailadres is al in gebruik.")
	case errors.Is(err, ErrGroupInUse):
		httpserver.WriteError(w, http.StatusConflict, "group_in_use", "Deze groep bepaalt de toegang tot een app. Pas eerst die app aan.")
	case errors.Is(err, ErrInvalidImage):
		httpserver.WriteError(w, http.StatusUnprocessableEntity, "invalid_image", "Dit is geen bruikbare PNG- of JPEG-afbeelding (maximaal 8000×8000 pixels).")
	case errors.Is(err, ErrPasskeyRequired):
		httpserver.WriteError(w, http.StatusForbidden, "passkey_required", "Deze app vraagt om in te loggen met een passkey.")
	case errors.Is(err, ErrAppAccessDenied):
		httpserver.WriteError(w, http.StatusForbidden, "app_access_denied", "Je hebt geen toegang tot deze app.")
	case errors.Is(err, ErrGroupExists):
		httpserver.WriteError(w, http.StatusConflict, "group_exists", "Er bestaat al een groep met deze naam.")
	case errors.Is(err, ErrUserNotFound):
		httpserver.WriteError(w, http.StatusNotFound, "not_found", "Gebruiker niet gevonden.")
	case errors.Is(err, ErrWrongState):
		httpserver.WriteError(w, http.StatusConflict, "invalid_state", "Deze stap is nu niet mogelijk. Begin opnieuw met inloggen.")
	default:
		s.log.Error("identity request failed", "request_id", httpserver.RequestIDFrom(r.Context()), "error", err)
		httpserver.WriteError(w, http.StatusInternalServerError, "internal", "Er ging iets mis aan onze kant.")
	}
}

func (s *Service) handleLoginRecovery(w http.ResponseWriter, r *http.Request, sess Session, _ User) {
	var in codeRequest
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	token, err := s.VerifyRecoveryCode(r.Context(), clientFrom(r), sess, in.Code)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.setSessionCookie(w, token, sessionMaxTTL)
	httpserver.WriteJSON(w, http.StatusOK, sessionResponse{State: StateAuthenticated})
}

type ceremonyResponse struct {
	ChallengeID uuid.UUID       `json:"challenge_id"`
	Credential  json.RawMessage `json:"credential"`
}

func (s *Service) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	ceremony, err := s.BeginPasskeyLogin(r.Context(), clientFrom(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, ceremony)
}

func (s *Service) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	var in ceremonyResponse
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	token, err := s.FinishPasskeyLogin(r.Context(), clientFrom(r), in.ChallengeID, in.Credential)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.setSessionCookie(w, token, sessionMaxTTL)
	httpserver.WriteJSON(w, http.StatusOK, sessionResponse{State: StateAuthenticated})
}

func (s *Service) handleListPasskeys(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	passkeys, err := s.ListPasskeys(r.Context(), p.User.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Passkeys []Passkey `json:"passkeys"`
	}{passkeys})
}

func (s *Service) handlePasskeyRegistrationBegin(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	ceremony, err := s.BeginPasskeyRegistration(r.Context(), p.Session)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, ceremony)
}

func (s *Service) handlePasskeyRegistrationFinish(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ceremonyResponse
		Name string `json:"name"`
	}
	if !httpserver.DecodeJSON(w, r, &in) {
		return
	}
	p, _ := PrincipalFrom(r.Context())
	passkey, err := s.FinishPasskeyRegistration(r.Context(), clientFrom(r), p.Session, in.ChallengeID, in.Name, in.Credential)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, passkey)
}

// pathID parses the {id} URL parameter; an invalid ID is simply not found.
func pathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return uuid.Nil, ErrNotFound
	}
	return id, nil
}

func (s *Service) handleDeletePasskey(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err == nil {
		p, _ := PrincipalFrom(r.Context())
		err = s.DeletePasskey(r.Context(), clientFrom(r), p.Session, id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleRecoveryCodeStatus(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	n, err := s.RemainingRecoveryCodes(r.Context(), p.User.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Remaining int `json:"remaining"`
	}{n})
}

func (s *Service) handleGenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	codes, err := s.GenerateRecoveryCodes(r.Context(), clientFrom(r), p.Session)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Codes []string `json:"codes"`
	}{codes})
}

func (s *Service) handleListSessions(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	sessions, err := s.ListSessions(r.Context(), p.Session)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Sessions []SessionInfo `json:"sessions"`
	}{sessions})
}

func (s *Service) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	id, err := pathID(r)
	if err == nil {
		err = s.RevokeSession(r.Context(), clientFrom(r), p.Session, id)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if id == p.Session.ID {
		s.clearSessionCookie(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleRevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	n, err := s.RevokeOtherSessions(r.Context(), clientFrom(r), p.Session)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Revoked int `json:"revoked"`
	}{n})
}

func (s *Service) handleOwnGroups(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	groups, err := s.UserGroups(r.Context(), p.User.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, struct {
		Groups []GroupRef `json:"groups"`
	}{groups})
}
