package upgrade

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// InstallCommand is an internal entrypoint for sudo. It neither downloads
// files nor runs the supplied executable, and can only replace itself.
const InstallCommand = "upgrade-install"

func InstallHelper(ctx context.Context, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet(InstallCommand, flag.ContinueOnError)
	fs.SetOutput(stderr)
	source := fs.String("source", "", "verified download")
	digest := fs.String("sha256", "", "expected checksum")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || !filepath.IsAbs(*source) || !validDigest(*digest) {
		return errors.New("invalid upgrade installation arguments")
	}
	u := New(Options{})
	target, err := u.executable()
	if err != nil {
		return err
	}
	return replace(ctx, *source, target, strings.ToLower(*digest))
}

// replace hashes the destination copy, not a previous read of source, so the
// privileged helper verifies exactly the bytes it will rename into place.
func replace(ctx context.Context, source, target, digest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validDigest(digest) {
		return errors.New("invalid CLI checksum")
	}
	old, err := os.Lstat(target)
	if err != nil {
		return err
	}
	if !old.Mode().IsRegular() {
		return errors.New("installed CLI must be a regular file")
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > binaryLimit {
		return errors.New("downloaded CLI must be a regular file within the size limit")
	}
	file, err := os.CreateTemp(filepath.Dir(target), ".withhuman-upgrade-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(contextReader{ctx, input}, binaryLimit+1))
	if err != nil {
		return err
	}
	if n > binaryLimit || fmt.Sprintf("%x", hash.Sum(nil)) != digest {
		return errors.New("CLI checksum changed before installation")
	}
	// Preserve ownership even when sudo creates the replacement file.
	owner := old.Sys().(*syscall.Stat_t)
	created, err := file.Stat()
	if err != nil {
		return err
	}
	newOwner := created.Sys().(*syscall.Stat_t)
	if owner.Uid != newOwner.Uid || owner.Gid != newOwner.Gid {
		if err := file.Chown(int(owner.Uid), int(owner.Gid)); err != nil {
			return err
		}
	}
	if err := file.Chmod(old.Mode().Perm()); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	current, err := os.Lstat(target)
	if err != nil {
		return err
	}
	if !os.SameFile(old, current) {
		return errors.New("installed CLI changed during upgrade; run withhuman upgrade again")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(file.Name(), target)
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
