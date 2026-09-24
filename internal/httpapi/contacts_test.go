package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/domain"
)

// Contact methods are product surface. They must exist behind the session
// guard and must never appear on the AAP wire.
func TestContactMethodRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := NewRouter(Dependencies{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	for _, test := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/me/contact-methods", ""},
		{http.MethodPost, "/api/v1/me/contact-methods", `{"value":"a@example.com"}`},
		{http.MethodPost, "/api/v1/me/contact-methods/m-1/verify", `{"code":"123456"}`},
		{http.MethodPost, "/api/v1/me/contact-methods/m-1/resend", ""},
		{http.MethodDelete, "/api/v1/me/contact-methods/m-1", ""},
		{http.MethodGet, "/api/v1/me/notification-rules", ""},
		{http.MethodPut, "/api/v1/me/notification-rules/interrupt", `{"rules":[{"delay_seconds":0,"contact_method_id":null}]}`},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Origin", "")
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 from the session guard", recorder.Code)
			}
		})
	}

	for _, path := range []string{
		"/api/aap/v1/me/contact-methods",
		"/api/aap/v1/me/notification-rules",
		"/api/aap/v1/contact-methods",
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404: contact methods are not protocol surface", path, recorder.Code)
		}
	}
}

// The wire shape tells the page what it may offer: the account address is
// verified and not removable, an added address starts unconfirmed.
func TestContactMethodResponseShape(t *testing.T) {
	verifiedAt := time.Unix(1_700_000_000, 0).UTC()
	primary := newContactMethodResponse(domain.ContactMethod{
		ID: "primary", Kind: domain.ContactEmail, Value: "owner@example.com",
		VerifiedAt: &verifiedAt, Primary: true, CreatedAt: verifiedAt,
	})
	added := newContactMethodResponse(domain.ContactMethod{
		ID: "m-1", Kind: domain.ContactEmail, Value: "personal@example.com",
		CreatedAt: verifiedAt,
	})
	if !primary.Verified || !primary.Primary {
		t.Fatalf("account address = %+v, want verified and primary", primary)
	}
	if added.Verified || added.Primary {
		t.Fatalf("added address = %+v, want unconfirmed and not primary", added)
	}
	encoded, err := json.Marshal(added)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "verified_at") {
		t.Fatalf("an unconfirmed address carries a verification time: %s", encoded)
	}
}
