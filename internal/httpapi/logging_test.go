package httpapi

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/loggermdc"
)

func TestConcurrentRequestsKeepGlobalAndAccessLogsIsolated(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	var output bytes.Buffer
	logger := loggermdc.Configure(slog.NewJSONHandler(&output, nil))
	router := NewRouter(Dependencies{Logger: logger})
	router.GET("/correlation/:id", func(c *gin.Context) {
		setPrincipal(c, domain.Principal{OrganizationID: domain.ID("org-" + c.Param("id")), ActorType: domain.ActorHuman, ActorID: domain.ID("user-" + c.Param("id"))})
		slog.WarnContext(c.Request.Context(), "service warning")
		c.Status(http.StatusNoContent)
	})
	var wait sync.WaitGroup
	for index := 0; index < 20; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			id := fmt.Sprint(index)
			request := httptest.NewRequest(http.MethodGet, "/correlation/"+id, nil)
			request.Header.Set("X-Request-ID", id)
			router.ServeHTTP(httptest.NewRecorder(), request)
		}()
	}
	wait.Wait()
	records := decodeLogRecords(t, &output)
	if len(records) != 40 {
		t.Fatalf("got %d records, want 40", len(records))
	}
	for _, record := range records {
		id, _ := record["request_id"].(string)
		if id == "" || record["trace_id"] != id || record["organization_id"] != "org-"+id || record["actor_id"] != "user-"+id {
			t.Fatalf("request identity leaked or missing: %#v", record)
		}
	}
}

func TestPanicAndAccessLogsShareEnrichedMDC(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var output bytes.Buffer
	router := NewRouter(Dependencies{Logger: slog.New(slog.NewJSONHandler(&output, nil))})
	router.GET("/panic", func(c *gin.Context) {
		setPrincipal(c, domain.Principal{OrganizationID: "org-1", ActorType: domain.ActorHuman, ActorID: "user-1"})
		panic("test panic")
	})
	request := httptest.NewRequest(http.MethodGet, "/panic", nil)
	request.Header.Set("X-Request-ID", "panic-request")
	request.Header.Set("Cookie", "session=secret-session")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"request_id":"panic-request"`) {
		t.Fatalf("unexpected panic response: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(output.String(), "secret-session") {
		t.Fatal("panic recovery logged a credential")
	}
	records := decodeLogRecords(t, &output)
	if len(records) != 2 || records[0]["msg"] != "http request panicked - GET /panic" || records[1]["msg"] != "http request - /panic" {
		t.Fatalf("missing panic or completion record: %#v", records)
	}
	for _, record := range records {
		if record["request_id"] != "panic-request" || record["organization_id"] != "org-1" || record["actor_id"] != "user-1" {
			t.Fatalf("panic correlation missing: %#v", record)
		}
	}
}
