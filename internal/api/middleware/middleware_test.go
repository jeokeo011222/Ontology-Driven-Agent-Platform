package middleware

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
	"odap/internal/platform/tenantctx"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestRequestIDGeneratesAndPropagates(t *testing.T) {
	var seen string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestIDFromContext(r.Context())
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if seen == "" || rec.Header().Get(HeaderRequestID) != seen {
		t.Fatalf("request id not generated/propagated: ctx=%q header=%q", seen, rec.Header().Get(HeaderRequestID))
	}
}

func TestRequestIDRejectsUnsafePassthrough(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(HeaderRequestID, "evil\ninjected")
	var got string
	RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = RequestIDFromContext(r.Context())
	})).ServeHTTP(rec, req)
	if got == "evil\ninjected" {
		t.Fatalf("unsafe request id was accepted")
	}
}

func TestRecoverReturnsSanitizedInternal(t *testing.T) {
	h := Recover(discardLogger())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret db password leaked")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rec.Code)
	}
	if body := rec.Body.String(); body == "" || contains(body, "secret db password") {
		t.Fatalf("panic detail leaked: %q", body)
	}
}

func TestRequireIdentityFailsClosed(t *testing.T) {
	rec := httptest.NewRecorder()
	RequireIdentity(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatalf("handler must not run without identity")
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}
}

func TestDevResolverParsesHeaders(t *testing.T) {
	tenant := ids.New()
	subject := ids.New()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(HeaderDevTenant, tenant.String())
	req.Header.Set(HeaderDevSubject, subject.String())
	req.Header.Set(HeaderDevScopes, "TENANT, ORG:abc")
	id, err := DevResolver{}.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if id.TenantID != tenant || id.SubjectID != subject {
		t.Fatalf("identity mismatch: %+v", id)
	}
	if len(id.Scopes) != 2 {
		t.Fatalf("scopes=%v", id.Scopes)
	}
}

func TestSessionTokenParsing(t *testing.T) {
	tenant := ids.New()
	session := ids.New()
	tok := "v1." + tenant.String() + "." + session.String() + ".7"
	gotTenant, gotSession, ver, err := parseSessionToken(tok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if gotTenant != tenant || gotSession != session || ver != 7 {
		t.Fatalf("parsed mismatch: %v %v %d", gotTenant, gotSession, ver)
	}
	for _, bad := range []string{"", "v1.x.y.1", "v2." + tenant.String() + "." + session.String() + ".1", "v1." + tenant.String() + "." + session.String() + ".0"} {
		if _, _, _, err := parseSessionToken(bad); err == nil {
			t.Fatalf("expected error for token %q", bad)
		}
	}
}

func TestAuthInjectsIdentity(t *testing.T) {
	tenant := ids.New()
	subject := ids.New()
	resolver := stubResolver{id: tenantctx.Identity{TenantID: tenant, SubjectID: subject}}
	var got tenantctx.Identity
	h := Auth(resolver)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = tenantctx.FromContext(r.Context())
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if got.TenantID != tenant {
		t.Fatalf("identity not injected: %+v", got)
	}
}

func TestAuthFailureIsUnauthenticated(t *testing.T) {
	resolver := stubResolver{err: errors.E(errors.CodeInternal, "db down")}
	rec := httptest.NewRecorder()
	Auth(resolver)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatalf("handler must not run")
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("internal auth error must collapse to 401, got %d", rec.Code)
	}
}

func TestCORSPreflightOnlyForWhitelistedOrigin(t *testing.T) {
	h := CORS(DefaultDevCORS())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/x", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Fatalf("preflight failed: %d %q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/x", nil)
	req2.Header.Set("Origin", "http://evil.example")
	h.ServeHTTP(rec2, req2)
	if rec2.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("non-whitelisted origin was allowed")
	}
}

func TestRateLimitRejectsAfterBurst(t *testing.T) {
	h := RateLimit(2, 0.0001, func(*http.Request) string { return "k" })(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	ok, limited := 0, 0
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
		switch rec.Code {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			limited++
			if rec.Header().Get("Retry-After") == "" {
				t.Fatalf("Retry-After missing")
			}
		}
	}
	if ok != 2 || limited != 3 {
		t.Fatalf("ok=%d limited=%d want 2/3", ok, limited)
	}
}

type stubResolver struct {
	id  tenantctx.Identity
	err error
}

func (s stubResolver) Resolve(context.Context, *http.Request) (tenantctx.Identity, error) {
	return s.id, s.err
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
