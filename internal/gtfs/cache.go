package gtfs

import (
	"context"
	"net/http"
	"time"

	"brnotransport/internal/filecache"
)

type Cache struct {
	URL      string
	Path     string
	TTL      time.Duration
	MaxBytes int64
	Client   *http.Client
}

func (c *Cache) file() *filecache.File {
	return &filecache.File{URL: c.URL, Path: c.Path, TTL: c.TTL, MaxBytes: c.MaxBytes, Client: c.Client}
}

func (c *Cache) Age() (time.Duration, bool) { return c.file().Age() }

func (c *Cache) Stale() bool { return c.file().Stale() }

func (c *Cache) LoadCached() (*Feed, error) { return Load(c.Path) }

func (c *Cache) Fetch(ctx context.Context) (*Feed, error) {
	var feed *Feed
	err := c.file().Fetch(ctx, func(path string) error {
		f, err := Load(path)
		feed = f
		return err
	})
	if err != nil {
		return nil, err
	}
	return feed, nil
}
