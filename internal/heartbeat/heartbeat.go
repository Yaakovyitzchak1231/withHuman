// Package heartbeat reports a deployment to withHuman once a day: its
// random installation ID, its version, and how it runs. The answer names
// the latest release and any security advisories affecting this version,
// which the product shows administrators. Nothing about organizations,
// people, agents or requests is ever sent.
package heartbeat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

// DefaultURL is where the open edition reports.
const DefaultURL = "https://app.withhuman.ai/api/installations/v1/heartbeat"

const (
	// checkInterval is how often a replica asks whether a send is due;
	// the claim in the database spaces the sends themselves.
	checkInterval  = time.Hour
	requestTimeout = 10 * time.Second
	maxAnswerBytes = 64 << 10
	maxAdvisories  = 20
	maxFieldLength = 500
)

// Request is the whole heartbeat body.
type Request struct {
	InstallationID domain.ID `json:"installation_id"`
	Version        string    `json:"version"`
	OS             string    `json:"os"`
	Arch           string    `json:"arch"`
	Deployment     string    `json:"deployment"`
}

// Answer is what the release service replies.
type Answer struct {
	LatestVersion string                    `json:"latest_version"`
	Advisories    []domain.SecurityAdvisory `json:"advisories"`
}

type Reporter struct {
	installations application.InstallationRepository
	client        *http.Client
	clock         application.Clock
	logger        *slog.Logger
	url           string
	version       string
}

func NewReporter(installations application.InstallationRepository, clock application.Clock, logger *slog.Logger, url, version string) *Reporter {
	return &Reporter{
		installations: installations, client: &http.Client{Timeout: requestTimeout},
		clock: clock, logger: logger, url: url, version: version,
	}
}

// Run reports whenever a send is due until ctx ends.
func (r *Reporter) Run(ctx context.Context) {
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()
	for {
		if err := r.Report(ctx); err != nil && ctx.Err() == nil {
			r.logger.InfoContext(ctx, "installation heartbeat failed; telemetry tries again later", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Report sends one heartbeat when this replica wins the claim, and keeps
// the answer. A failed send is not retried before the next interval.
func (r *Reporter) Report(ctx context.Context) error {
	id, claimed, err := r.installations.ClaimHeartbeat(ctx, application.ClaimHeartbeatInput{Now: r.clock.Now(), Version: r.version})
	if err != nil {
		return fmt.Errorf("claim the heartbeat: %w", err)
	}
	if !claimed {
		return nil
	}
	body, err := json.Marshal(Request{
		InstallationID: id, Version: r.version, OS: goruntime.GOOS, Arch: goruntime.GOARCH, Deployment: deployment(),
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "withhuman/"+r.version)
	response, err := r.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("release service answered %d", response.StatusCode)
	}
	var answer Answer
	if err := json.NewDecoder(io.LimitReader(response.Body, maxAnswerBytes)).Decode(&answer); err != nil {
		return fmt.Errorf("decode answer: %w", err)
	}
	if err := answer.validate(); err != nil {
		return err
	}
	return r.installations.RecordHeartbeatAnswer(ctx, application.RecordHeartbeatAnswerInput{
		CheckedAt: r.clock.Now(), LatestVersion: answer.LatestVersion, Advisories: answer.Advisories,
	})
}

// validate refuses an answer the product could not show safely: the
// advisories become links in the app, so each must be plain https.
func (a Answer) validate() error {
	if !ReleaseVersion(a.LatestVersion) {
		return errors.New("answer's latest version is not a release version")
	}
	if len(a.Advisories) > maxAdvisories {
		return errors.New("answer lists too many advisories")
	}
	for _, advisory := range a.Advisories {
		for _, field := range []string{advisory.ID, advisory.Summary, advisory.Severity, advisory.URL, advisory.FixedIn} {
			if len(field) > maxFieldLength {
				return errors.New("answer has an advisory field that is too long")
			}
		}
		link, err := url.Parse(advisory.URL)
		if err != nil || link.Scheme != "https" || link.Host == "" {
			return errors.New("answer has an advisory without an https link")
		}
		if advisory.FixedIn != "" && !ReleaseVersion(advisory.FixedIn) {
			return errors.New("answer has an advisory whose fixed version is not a release version")
		}
	}
	return nil
}

// ReleaseVersion reports whether version is a tagged release
// (major.minor.patch), as opposed to a development build.
func ReleaseVersion(version string) bool {
	_, ok := parseVersion(version)
	return ok
}

// Newer reports whether candidate is a later release than current. A
// version that is not a release is never newer, and never outdated.
func Newer(candidate, current string) bool {
	c, ok := parseVersion(candidate)
	if !ok {
		return false
	}
	v, ok := parseVersion(current)
	if !ok {
		return false
	}
	for i := range c {
		if c[i] != v[i] {
			return c[i] > v[i]
		}
	}
	return false
}

func parseVersion(version string) ([3]int, bool) {
	var parsed [3]int
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return parsed, false
	}
	for i, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 || part != strconv.Itoa(number) {
			return parsed, false
		}
		parsed[i] = number
	}
	return parsed, true
}

// deployment names how the process runs, from what the platform leaves
// in its environment.
func deployment() string {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return "kubernetes"
	}
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return "container"
		}
	}
	return "binary"
}
