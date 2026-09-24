package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const noticeInterval = 24 * time.Hour

type noticeCache struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest_version"`
}

// Notice is best effort. It never changes the surrounding command's result.
func (u *Updater) Notice(ctx context.Context) {
	if !u.env.Interactive() || ctx.Err() != nil {
		return
	}
	if _, valid := parseVersion(u.opts.Version); !valid {
		return
	}
	base, err := releaseBase(u.opts.BaseURL)
	if err != nil {
		return
	}
	path := filepath.Join(u.opts.ConfigDir, "updates", fmt.Sprintf("%x.json", sha256.Sum256([]byte(base))))
	cached := readNotice(path)
	now := u.env.Now()
	age := now.Sub(cached.CheckedAt)
	if cached.CheckedAt.IsZero() || age < 0 || age >= noticeInterval {
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		latest, err := u.latest(ctx, base)
		cancel()
		if err == nil {
			cached.Latest = latest
		}
		// Cache failed attempts too, so offline use does not add a timeout
		// to every interactive command. Keep any previously known release.
		cached.CheckedAt = now
		writeNotice(path, cached)
	}
	if newer(cached.Latest, u.opts.Version) {
		fmt.Fprintf(u.opts.Stderr, "withhuman %s is available (installed: %s). Run withhuman upgrade.\n", cached.Latest, u.opts.Version)
	}
}

func readNotice(path string) noticeCache {
	file, err := os.Open(path)
	if err != nil {
		return noticeCache{}
	}
	defer file.Close()
	var cached noticeCache
	if json.NewDecoder(io.LimitReader(file, 4096)).Decode(&cached) != nil {
		return noticeCache{}
	}
	return cached
}

func writeNotice(path string, cached noticeCache) {
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".update-*")
	if err != nil {
		return
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if json.NewEncoder(file).Encode(cached) != nil {
		return
	}
	if file.Close() != nil {
		return
	}
	_ = os.Rename(file.Name(), path)
}
