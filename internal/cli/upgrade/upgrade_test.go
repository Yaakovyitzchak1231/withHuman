package upgrade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const testVersion = "0.0.225"
const testAsset = "withhuman-linux-arm64"

type releaseResponse struct {
	status int
	body   string
}

func releaseResponses(binary string) map[string]releaseResponse {
	return map[string]releaseResponse{
		"/cli/latest/version":                   {200, testVersion + "\n"},
		"/cli/" + testVersion + "/SHA256SUMS":   {200, fmt.Sprintf("%x  %s\n", sha256.Sum256([]byte(binary)), testAsset)},
		"/cli/" + testVersion + "/" + testAsset: {200, binary},
	}
}

func releaseServer(t *testing.T, responses map[string]releaseResponse) (string, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var requests []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Path)
		mu.Unlock()
		response, ok := responses[r.URL.Path]
		if !ok {
			t.Errorf("unexpected release request: %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(response.status)
		io.WriteString(w, response.body)
	}))
	t.Cleanup(s.Close)
	return s.URL, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), requests...)
	}
}

func testOptions(t *testing.T, base string) (Options, string) {
	t.Helper()
	target := filepath.Join(t.TempDir(), "withhuman")
	if err := os.WriteFile(target, []byte("original CLI"), 0751); err != nil {
		t.Fatal(err)
	}
	var err error
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	return Options{
		Version: "0.0.198", ConfigDir: t.TempDir(), BaseURL: base,
		Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
		Env: Environment{
			GOOS: "linux", GOARCH: "arm64", Executable: func() (string, error) { return target, nil },
			Run: func(_ context.Context, _ string, args []string, _ io.Reader, stdout, _ io.Writer) error {
				if !reflect.DeepEqual(args, []string{"version"}) {
					t.Fatalf("unexpected subprocess: %v", args)
				}
				_, err := fmt.Fprintln(stdout, "withhuman", testVersion)
				return err
			},
		},
	}, target
}

func TestUpgradeVerifiedImmutableRelease(t *testing.T) {
	for _, current := range []string{"0.0.198", "dev"} {
		t.Run(current, func(t *testing.T) {
			base, requests := releaseServer(t, releaseResponses("new CLI"))
			opts, target := testOptions(t, base)
			opts.Version = current
			link := filepath.Join(t.TempDir(), "CLI link")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			opts.Env.Executable = func() (string, error) { return link, nil }
			if err := New(opts).Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertFile(t, target, "new CLI")
			assertFile(t, link, "new CLI")
			if info, _ := os.Lstat(link); info.Mode()&os.ModeSymlink == 0 {
				t.Fatal("replaced the symlink")
			}
			if info, _ := os.Stat(target); info.Mode().Perm() != 0751 {
				t.Fatal("changed executable permissions")
			}
			want := []string{"/cli/latest/version", "/cli/" + testVersion + "/SHA256SUMS", "/cli/" + testVersion + "/" + testAsset}
			if !reflect.DeepEqual(requests(), want) {
				t.Fatalf("requests: %v", requests())
			}
			if !strings.Contains(opts.Stdout.(*bytes.Buffer).String(), "from "+current+" to "+testVersion) {
				t.Fatal("missing success report")
			}
		})
	}
}

func TestUpgradeKeepsCurrentOrNewerCLI(t *testing.T) {
	for _, current := range []string{testVersion, "0.0.1000", "0.1.0", "1.0.0"} {
		t.Run(current, func(t *testing.T) {
			base, requests := releaseServer(t, releaseResponses("new CLI"))
			opts, target := testOptions(t, base)
			opts.Version = current
			if err := New(opts).Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertFile(t, target, "original CLI")
			if !reflect.DeepEqual(requests(), []string{"/cli/latest/version"}) {
				t.Fatalf("downloaded an unnecessary release: %v", requests())
			}
		})
	}
}

func TestUpgradeFailuresKeepInstalledCLI(t *testing.T) {
	for _, scenario := range []string{"metadata", "http", "missing-checksum", "invalid-checksum", "duplicate-checksum", "wrong-checksum", "wrong-version", "execution", "cancelled", "replace", "unsupported"} {
		t.Run(scenario, func(t *testing.T) {
			responses := releaseResponses("new CLI")
			manifest := "/cli/" + testVersion + "/SHA256SUMS"
			switch scenario {
			case "metadata":
				responses["/cli/latest/version"] = releaseResponse{200, "../unsafe"}
			case "http":
				responses["/cli/"+testVersion+"/"+testAsset] = releaseResponse{503, "unavailable"}
			case "missing-checksum":
				responses[manifest] = releaseResponse{200, ""}
			case "invalid-checksum":
				responses[manifest] = releaseResponse{200, "bad  " + testAsset}
			case "duplicate-checksum":
				responses[manifest] = releaseResponse{200, responses[manifest].body + responses[manifest].body}
			case "wrong-checksum":
				responses["/cli/"+testVersion+"/"+testAsset] = releaseResponse{200, "tampered CLI"}
			}
			base, _ := releaseServer(t, responses)
			opts, target := testOptions(t, base)
			downloadDir := t.TempDir()
			t.Setenv("TMPDIR", downloadDir)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var downloaded string
			verify := opts.Env.Run
			opts.Env.Run = func(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
				downloaded = name
				switch scenario {
				case "wrong-version":
					fmt.Fprintln(stdout, "withhuman 0.0.1")
					return nil
				case "execution":
					return errors.New("invalid executable")
				case "cancelled":
					cancel()
				}
				return verify(ctx, name, args, stdin, stdout, stderr)
			}
			if scenario == "replace" {
				opts.Env.Replace = func(context.Context, string, string, string) error { return errors.New("disk full") }
			}
			if scenario == "unsupported" {
				opts.Env.GOOS = "windows"
			}
			if err := New(opts).Run(ctx); err == nil {
				t.Fatal("accepted failed upgrade")
			}
			assertFile(t, target, "original CLI")
			if strings.Contains(opts.Stdout.(*bytes.Buffer).String(), "Upgraded") {
				t.Fatal("reported success on failure")
			}
			if scenario == "wrong-checksum" && downloaded != "" {
				t.Fatal("executed a download with an invalid checksum")
			}
			if files, err := os.ReadDir(downloadDir); err != nil || len(files) != 0 {
				t.Fatalf("left a download behind: %v %v", files, err)
			}
			if downloaded != "" {
				if _, err := os.Stat(filepath.Dir(downloaded)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("left downloaded files behind")
				}
			}
		})
	}
}

func TestUpgradeSudoHandoff(t *testing.T) {
	for _, scenario := range []string{"success", "refused", "failed", "noninteractive", "tampered"} {
		t.Run(scenario, func(t *testing.T) {
			base, _ := releaseServer(t, releaseResponses("new CLI"))
			opts, target := testOptions(t, base)
			opts.Env.Interactive = func() bool { return scenario != "noninteractive" }
			opts.Env.Replace = func(context.Context, string, string, string) error { return os.ErrPermission }
			verify := opts.Env.Run
			calls := 0
			var download string
			opts.Env.Run = func(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
				if name != "sudo" {
					return verify(ctx, name, args, stdin, stdout, stderr)
				}
				calls++
				if len(args) != 7 || !reflect.DeepEqual(args[:4], []string{"--", target, InstallCommand, "--source"}) || args[5] != "--sha256" {
					t.Fatalf("unsafe sudo arguments: %v", args)
				}
				download = args[4]
				if scenario == "refused" || scenario == "failed" {
					return errors.New("sudo " + scenario)
				}
				if scenario == "tampered" {
					if err := os.WriteFile(download, []byte("tampered"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				return replace(ctx, download, target, args[6])
			}
			err := New(opts).Run(context.Background())
			if (err == nil) != (scenario == "success") {
				t.Fatalf("unexpected result: %v", err)
			}
			if scenario == "success" {
				assertFile(t, target, "new CLI")
			} else {
				assertFile(t, target, "original CLI")
			}
			if scenario == "noninteractive" {
				if calls != 0 || !strings.Contains(err.Error(), "sudo env WITHHUMAN_CLI_BASE=") || !strings.Contains(err.Error(), target) {
					t.Fatalf("missing noninteractive guidance: %v", err)
				}
			} else if calls != 1 {
				t.Fatalf("sudo calls: %d", calls)
			}
			if download != "" {
				if _, err := os.Stat(download); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("left sudo source behind")
				}
			}
		})
	}
}

func TestReplaceFailureCleanup(t *testing.T) {
	for _, scenario := range []string{"checksum", "cancelled", "target-symlink", "directory-source", "permission"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			source, target := filepath.Join(t.TempDir(), "source"), filepath.Join(dir, "withhuman")
			os.WriteFile(source, []byte("new CLI"), 0700)
			os.WriteFile(target, []byte("original CLI"), 0755)
			digest := fmt.Sprintf("%x", sha256.Sum256([]byte("new CLI")))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "checksum":
				digest = strings.Repeat("0", 64)
			case "cancelled":
				cancel()
			case "target-symlink":
				link := filepath.Join(dir, "link")
				os.Symlink(target, link)
				target = link
			case "directory-source":
				source = t.TempDir()
			case "permission":
				if os.Geteuid() == 0 {
					t.Skip("root bypasses directory permissions")
				}
				os.Chmod(dir, 0500)
				defer os.Chmod(dir, 0700)
			}
			err := replace(ctx, source, target, digest)
			if err == nil {
				t.Fatal("accepted failed replacement")
			}
			if scenario == "permission" && !errors.Is(err, os.ErrPermission) {
				t.Fatalf("lost permission error: %v", err)
			}
			assertFile(t, target, "original CLI")
			files, _ := filepath.Glob(filepath.Join(dir, ".withhuman-upgrade-*"))
			if len(files) != 0 {
				t.Fatalf("left partial replacements: %v", files)
			}
		})
	}
}

func TestReleaseValidationAndNumericOrdering(t *testing.T) {
	for _, value := range []string{"", "v1.2.3", "1.2", "1.2.3-rc.1", "01.2.3", "../1.2.3", "18446744073709551616.1.0"} {
		if _, ok := parseVersion(value); ok {
			t.Errorf("accepted release %q", value)
		}
	}
	if !newer("0.0.100", "0.0.99") || newer("0.0.9", "0.0.10") || newer("1.0.0", "1.0.0") {
		t.Fatal("versions are not compared numerically")
	}
	for _, value := range []string{"http://downloads.example.com", "https://user:pass@example.com", "https://example.com?token=secret", "https://example.com/#fragment"} {
		if _, err := releaseBase(value); err == nil {
			t.Errorf("accepted source %q", value)
		}
	}
	if base, err := releaseBase("https://example.com/releases/"); err != nil || base != "https://example.com/releases" {
		t.Fatal(base, err)
	}
}

func TestReleaseNetworkFailureAndCancellation(t *testing.T) {
	for _, scenario := range []string{"offline", "cancelled", "short-body", "redirect"} {
		t.Run(scenario, func(t *testing.T) {
			opts, target := testOptions(t, "https://downloads.example.com")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			opts.HTTP = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				switch scenario {
				case "offline":
					return nil, errors.New("network unavailable")
				case "cancelled":
					cancel()
					return nil, r.Context().Err()
				case "short-body":
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(brokenReader{})}, nil
				default:
					return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"http://unsafe.example.com"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
			})}
			if err := New(opts).Run(ctx); err == nil {
				t.Fatal("accepted failed download")
			}
			assertFile(t, target, "original CLI")
		})
	}
}

func TestCancellationDuringDownloadCleansUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	responses := releaseResponses("new CLI")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response, ok := responses[r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		if r.URL.Path == "/cli/"+testVersion+"/"+testAsset {
			w.Header().Set("Content-Length", "7")
			io.WriteString(w, "new")
			w.(http.Flusher).Flush()
			cancel()
			<-r.Context().Done()
			return
		}
		io.WriteString(w, response.body)
	}))
	defer server.Close()
	opts, target := testOptions(t, server.URL)
	downloadDir := t.TempDir()
	t.Setenv("TMPDIR", downloadDir)
	err := New(opts).Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	assertFile(t, target, "original CLI")
	if files, err := os.ReadDir(downloadDir); err != nil || len(files) != 0 {
		t.Fatalf("left a partial download: %v %v", files, err)
	}
}

func TestOversizedMetadataIsRejected(t *testing.T) {
	responses := releaseResponses("new CLI")
	responses["/cli/latest/version"] = releaseResponse{200, testVersion + strings.Repeat(" ", 128)}
	base, _ := releaseServer(t, responses)
	opts, target := testOptions(t, base)
	if err := New(opts).Run(context.Background()); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("accepted oversized metadata: %v", err)
	}
	assertFile(t, target, "original CLI")
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != want {
		t.Fatalf("%s: got %q, want %q, err %v", path, raw, want, err)
	}
}

func TestNoticeCacheAndSourceIsolation(t *testing.T) {
	base, requests := releaseServer(t, releaseResponses("new CLI"))
	other, otherRequests := releaseServer(t, releaseResponses("new CLI"))
	opts, _ := testOptions(t, base)
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	opts.Env.Now = func() time.Time { return now }
	opts.Env.Interactive = func() bool { return true }
	u := New(opts)
	u.Notice(context.Background())
	u.Notice(context.Background())
	if len(requests()) != 1 || strings.Count(opts.Stderr.(*bytes.Buffer).String(), "Run withhuman upgrade.") != 2 {
		t.Fatal("did not reuse update check", requests(), opts.Stderr)
	}
	now = now.Add(noticeInterval)
	u.Notice(context.Background())
	if len(requests()) != 2 {
		t.Fatal("did not refresh after 24 hours")
	}
	opts.BaseURL = other
	New(opts).Notice(context.Background())
	if len(otherRequests()) != 1 {
		t.Fatal("shared cache across download sources")
	}
	opts.Version = testVersion
	opts.Stderr.(*bytes.Buffer).Reset()
	New(opts).Notice(context.Background())
	if opts.Stderr.(*bytes.Buffer).Len() != 0 {
		t.Fatal("announced an already installed release")
	}
}

func TestNoticeFailureIsSilentAndCached(t *testing.T) {
	opts, _ := testOptions(t, "https://downloads.example.com")
	opts.Env.Interactive = func() bool { return true }
	calls := 0
	opts.HTTP = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > time.Second {
			t.Error("update check is not bounded to one second")
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	start := time.Now()
	u := New(opts)
	u.Notice(context.Background())
	u.Notice(context.Background())
	if calls != 1 || time.Since(start) > 3*time.Second || opts.Stderr.(*bytes.Buffer).Len() != 0 {
		t.Fatal("failed checks affected output or were not cached", calls, opts.Stderr)
	}
}

func TestNoticeSkipsDevelopmentAndNoninteractiveCommands(t *testing.T) {
	base, requests := releaseServer(t, releaseResponses("new CLI"))
	for _, scenario := range []string{"dev", "noninteractive"} {
		opts, _ := testOptions(t, base)
		opts.Env.Interactive = func() bool { return scenario != "noninteractive" }
		if scenario == "dev" {
			opts.Version = "dev"
		}
		New(opts).Notice(context.Background())
		if opts.Stderr.(*bytes.Buffer).Len() != 0 {
			t.Fatal("unexpected notice")
		}
	}
	if len(requests()) != 0 {
		t.Fatal("unexpected network check", requests())
	}
}

func TestNoticeCorruptCacheAndUnwritableCache(t *testing.T) {
	for _, scenario := range []string{"corrupt", "unwritable", "future"} {
		t.Run(scenario, func(t *testing.T) {
			base, requests := releaseServer(t, releaseResponses("new CLI"))
			opts, _ := testOptions(t, base)
			opts.Env.Interactive = func() bool { return true }
			path := filepath.Join(opts.ConfigDir, "updates", fmt.Sprintf("%x.json", sha256.Sum256([]byte(base))))
			if scenario == "unwritable" {
				os.WriteFile(filepath.Dir(path), []byte("not a directory"), 0600)
			} else {
				os.MkdirAll(filepath.Dir(path), 0700)
				if scenario == "corrupt" {
					os.WriteFile(path, []byte("{broken"), 0600)
				} else {
					writeNotice(path, noticeCache{CheckedAt: time.Now().Add(time.Hour), Latest: "0.0.1"})
				}
			}
			New(opts).Notice(context.Background())
			if len(requests()) != 1 || !strings.Contains(opts.Stderr.(*bytes.Buffer).String(), "Run withhuman upgrade.") {
				t.Fatal("cache failure prevented notice", requests(), opts.Stderr)
			}
		})
	}
}
