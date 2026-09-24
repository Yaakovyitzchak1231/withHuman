// Package upgrade downloads verified CLI releases and replaces the executable.
package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const defaultBaseURL = "https://downloads.withhuman.ai"
const binaryLimit = 128 << 20

// Releases use three numeric components, without a leading v or prerelease tag.
var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func parseVersion(value string) ([3]uint64, bool) {
	var version [3]uint64
	if !versionPattern.MatchString(value) {
		return version, false
	}
	for i, part := range strings.Split(value, ".") {
		var err error
		version[i], err = strconv.ParseUint(part, 10, 64)
		if err != nil {
			return version, false
		}
	}
	return version, true
}

func newer(latest, current string) bool {
	l, validLatest := parseVersion(latest)
	c, validCurrent := parseVersion(current)
	if !validLatest || !validCurrent {
		return false
	}
	for i := range l {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func releaseBase(value string) (string, error) {
	if value == "" {
		return defaultBaseURL, nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("WITHHUMAN_CLI_BASE must be an HTTPS URL without credentials, a query, or a fragment")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return "", errors.New("WITHHUMAN_CLI_BASE requires HTTPS except on localhost")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func (u *Updater) fetch(ctx context.Context, address string, dst io.Writer, limit int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", address, resp.StatusCode)
	}
	n, err := io.Copy(dst, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return fmt.Errorf("download %s exceeds the size limit", address)
	}
	return ctx.Err()
}

func (u *Updater) latest(ctx context.Context, base string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var body strings.Builder
	if err := u.fetch(ctx, base+"/cli/latest/version", &body, 128); err != nil {
		return "", err
	}
	version := strings.TrimSpace(body.String())
	if _, ok := parseVersion(version); !ok {
		return "", errors.New("release server returned an invalid version")
	}
	return version, nil
}

func (u *Updater) checksum(ctx context.Context, base, version, filename string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var body strings.Builder
	if err := u.fetch(ctx, base+"/cli/"+version+"/SHA256SUMS", &body, 64<<10); err != nil {
		return "", err
	}
	var digest string
	for _, line := range strings.Split(body.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != filename {
			continue
		}
		if digest != "" || !validDigest(fields[0]) {
			return "", fmt.Errorf("invalid checksum entry for %s", filename)
		}
		digest = strings.ToLower(fields[0])
	}
	if digest == "" {
		return "", fmt.Errorf("SHA256SUMS has no entry for %s", filename)
	}
	return digest, nil
}

func validDigest(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == sha256.Size
}
