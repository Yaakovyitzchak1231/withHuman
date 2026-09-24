package upgrade

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Environment isolates OS interactions so upgrades can be tested without
// changing the installed CLI or invoking a real privilege prompt.
type Environment struct {
	Now          func() time.Time
	Executable   func() (string, error)
	Interactive  func() bool
	Run          func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error
	Replace      func(context.Context, string, string, string) error
	GOOS, GOARCH string
}

type Options struct {
	Version, ConfigDir, BaseURL string
	HTTP                        *http.Client
	Stdin                       io.Reader
	Stdout, Stderr              io.Writer
	Env                         Environment
}

type Updater struct {
	opts   Options
	client *http.Client
	env    Environment
}

func New(opts Options) *Updater {
	env := opts.Env
	if env.Now == nil {
		env.Now = time.Now
	}
	if env.Executable == nil {
		env.Executable = os.Executable
	}
	if env.Interactive == nil {
		env.Interactive = func() bool { return false }
	}
	if env.Run == nil {
		env.Run = runCommand
	}
	if env.Replace == nil {
		env.Replace = replace
	}
	if env.GOOS == "" {
		env.GOOS = runtime.GOOS
	}
	if env.GOARCH == "" {
		env.GOARCH = runtime.GOARCH
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	var client http.Client
	if opts.HTTP != nil {
		client = *opts.HTTP
	}
	if client.Timeout == 0 {
		client.Timeout = 5 * time.Minute
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Updater{opts: opts, client: &client, env: env}
}

func runCommand(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	// Allow the helper to remove its temporary destination on cancellation.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5 * time.Second
	return cmd.Run()
}

func (u *Updater) executable() (string, error) {
	path, err := u.env.Executable()
	if err != nil {
		return "", err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(path)
}

func (u *Updater) Run(ctx context.Context) error {
	if (u.env.GOOS != "darwin" && u.env.GOOS != "linux") || (u.env.GOARCH != "amd64" && u.env.GOARCH != "arm64") {
		return fmt.Errorf("CLI upgrades are unavailable for %s/%s; build the CLI from source", u.env.GOOS, u.env.GOARCH)
	}
	base, err := releaseBase(u.opts.BaseURL)
	if err != nil {
		return err
	}
	version, err := u.latest(ctx, base)
	if err != nil {
		return fmt.Errorf("check latest release: %w", err)
	}
	if _, valid := parseVersion(u.opts.Version); valid && !newer(version, u.opts.Version) {
		fmt.Fprintf(u.opts.Stdout, "withhuman %s is up to date (latest: %s).\n", u.opts.Version, version)
		return nil
	}
	target, err := u.executable()
	if err != nil {
		return fmt.Errorf("locate installed CLI: %w", err)
	}
	filename := "withhuman-" + u.env.GOOS + "-" + u.env.GOARCH
	digest, err := u.checksum(ctx, base, version, filename)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "withhuman-upgrade-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	source := filepath.Join(dir, "withhuman")
	fmt.Fprintf(u.opts.Stdout, "Downloading withhuman %s...\n", version)
	if err := u.download(ctx, base+"/cli/"+version+"/"+filename, source, digest); err != nil {
		return err
	}
	if err := u.verifyVersion(ctx, source, version); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := u.env.Replace(ctx, source, target, digest); err != nil {
		if !errors.Is(err, os.ErrPermission) || ctx.Err() != nil {
			return fmt.Errorf("replace installed CLI: %w", err)
		}
		if !u.env.Interactive() {
			return fmt.Errorf("administrator access is required; rerun in a terminal or run: sudo env WITHHUMAN_CLI_BASE=%s %s upgrade", shellQuote(base), shellQuote(target))
		}
		fmt.Fprintf(u.opts.Stderr, "Replacing %s requires administrator access.\n", target)
		if err := u.env.Run(ctx, "sudo", []string{"--", target, InstallCommand, "--source", source, "--sha256", digest}, u.opts.Stdin, u.opts.Stdout, u.opts.Stderr); err != nil {
			return fmt.Errorf("install upgrade with sudo: %w", err)
		}
	}
	fmt.Fprintf(u.opts.Stdout, "Upgraded withhuman from %s to %s.\n", u.opts.Version, version)
	return nil
}

func (u *Updater) download(ctx context.Context, address, path, digest string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if err := u.fetch(ctx, address, io.MultiWriter(file, hash), binaryLimit); err != nil {
		return fmt.Errorf("download CLI: %w", err)
	}
	if fmt.Sprintf("%x", hash.Sum(nil)) != digest {
		return errors.New("downloaded CLI checksum does not match SHA256SUMS")
	}
	if err := file.Chmod(0700); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return file.Close()
}

func (u *Updater) verifyVersion(ctx context.Context, path, version string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output := limitedOutput{remaining: 1024}
	if err := u.env.Run(ctx, path, []string{"version"}, nil, &output, io.Discard); err != nil {
		return fmt.Errorf("verify downloaded CLI version: %w", err)
	}
	if strings.TrimSpace(output.String()) != "withhuman "+version {
		return fmt.Errorf("downloaded executable does not report withhuman %s", version)
	}
	return nil
}

type limitedOutput struct {
	strings.Builder
	remaining int
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		return 0, errors.New("executable version output exceeds the size limit")
	}
	w.remaining -= len(p)
	return w.Builder.Write(p)
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
