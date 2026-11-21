package gtfs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type Cache struct {
	URL    string
	Path   string
	TTL    time.Duration
	Client *http.Client
}

func (c *Cache) Age() (time.Duration, bool) {
	fi, err := os.Stat(c.Path)
	if err != nil {
		return 0, false
	}
	return time.Since(fi.ModTime()), true
}

func (c *Cache) Stale() bool {
	age, ok := c.Age()
	return !ok || age >= c.TTL
}

func (c *Cache) LoadCached() (*Feed, error) {
	return Load(c.Path)
}

func (c *Cache) Fetch(ctx context.Context) (*Feed, error) {
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o755); err != nil {
		return nil, err
	}
	part := c.Path + ".part"
	if err := c.download(ctx, part); err != nil {
		os.Remove(part)
		return nil, err
	}
	feed, err := Load(part)
	if err != nil {
		os.Remove(part)
		return nil, fmt.Errorf("downloaded feed is invalid: %w", err)
	}
	if err := os.Rename(part, c.Path); err != nil {
		os.Remove(part)
		return nil, err
	}
	return feed, nil
}

func (c *Cache) download(ctx context.Context, dst string) error {
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", c.URL, resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
