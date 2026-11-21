package filecache

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const DefaultMaxBytes = 1 << 30

type File struct {
	URL      string
	Path     string
	TTL      time.Duration
	MaxBytes int64
	Client   *http.Client
}

func (f *File) Age() (time.Duration, bool) {
	fi, err := os.Stat(f.Path)
	if err != nil {
		return 0, false
	}
	return time.Since(fi.ModTime()), true
}

func (f *File) Stale() bool {
	age, ok := f.Age()
	return !ok || age >= f.TTL
}

func (f *File) Fetch(ctx context.Context, validate func(path string) error) error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return err
	}
	part := f.Path + ".part"
	if err := f.download(ctx, part); err != nil {
		os.Remove(part)
		return err
	}
	if err := validate(part); err != nil {
		os.Remove(part)
		return fmt.Errorf("downloaded file is invalid: %w", err)
	}
	if err := os.Rename(part, f.Path); err != nil {
		os.Remove(part)
		return err
	}
	return nil
}

func (f *File) download(ctx context.Context, dst string) error {
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", f.URL, resp.Status)
	}
	limit := f.MaxBytes
	if limit <= 0 {
		limit = DefaultMaxBytes
	}
	if resp.ContentLength > limit {
		return fmt.Errorf("download %s: size %d exceeds limit %d", f.URL, resp.ContentLength, limit)
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(resp.Body, limit+1))
	if err == nil && n > limit {
		err = fmt.Errorf("download %s: exceeds limit %d", f.URL, limit)
	}
	if err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
