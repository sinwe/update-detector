// Package selfupdate checks a GitHub repo's release list for the
// newest real (non-pre-release) tag of update-detector itself, so the
// aggregator can tell a fleet "vX.Y.Z is available."
package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"update-detector/internal/version"
)

const defaultGitHubAPI = "https://api.github.com/repos/sinwe/update-detector"

type release struct {
	TagName string `json:"tag_name"`
}

// Client checks a GitHub repo's release list for the newest release
// tag matching the configured channel, caching the result in memory.
// Safe for concurrent use.
type Client struct {
	apiBase string
	http    *http.Client

	mu        sync.Mutex
	channel   string // one of version.Channels; mutable via SetChannel, e.g. from an admin-page selector
	latestTag string
	fetchedAt time.Time
	hasResult bool
}

// New returns a Client for apiBase (e.g.
// "https://api.github.com/repos/sinwe/update-detector"), or this repo's
// own default if apiBase is empty. channel selects the
// minimum acceptable stage (one of version.Channels: "alpha", "beta",
// "rc", "release") -- "release" (the default, safer choice for a
// production fleet) considers only real releases; any pre-release
// channel also considers tags at that stage or more stable, and picks
// the single highest tag across all of them -- so an available alpha/
// beta/rc *always* outranks an older real release, on the assumption
// that anyone opting into a pre-release channel wants the newest build
// available at or above that stage. Panics if channel isn't one of
// version.Channels -- this is a startup-time configuration error, not a
// runtime condition to handle gracefully.
func New(apiBase, channel string) *Client {
	if apiBase == "" {
		apiBase = defaultGitHubAPI
	}
	if !version.ValidChannel(channel) {
		panic(fmt.Sprintf("selfupdate: invalid channel %q (want one of %v)", channel, version.Channels))
	}
	return &Client{apiBase: apiBase, channel: channel, http: &http.Client{Timeout: 15 * time.Second}}
}

// Refresh fetches the release list and updates the cached latest real
// release tag. A fetch failure (network, non-200, bad JSON, or no real
// release found at all) leaves any previously cached result untouched --
// a transient GitHub/network outage must not erase an otherwise-valid
// "update available" fact the admin page is showing.
//
// Deliberately not GET /releases/latest, even though GitHub's version of
// that endpoint (unlike Forgejo's) does correctly exclude prereleases:
// channel selection (see version.MeetsChannel) needs the *whole* list to
// find the highest release at or above a given stage, not just the
// single overall latest -- an alpha/beta/rc channel needs to see
// pre-release tags "latest" would filter out entirely.
//
// Fetches one page with a generous per_page rather than paginating --
// missing the true latest release would require more pre-release-only
// tags pushed after it than that page size, which isn't a realistic
// release cadence for this repo; a deliberate, accepted simplification.
func (c *Client) Refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase+"/releases?per_page=20", nil)
	if err != nil {
		return fmt.Errorf("selfupdate: building request: %w", err)
	}
	// GitHub's API rejects requests with no User-Agent at all (403), and
	// recommends this Accept value for the current API version.
	req.Header.Set("User-Agent", "update-detector-selfupdate")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("selfupdate: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("selfupdate: unexpected status %s", resp.Status)
	}

	var releases []release
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return fmt.Errorf("selfupdate: decoding response: %w", err)
	}

	latest, ok := highestRelease(releases, c.Channel())
	if !ok {
		return fmt.Errorf("selfupdate: no matching release found")
	}

	c.mu.Lock()
	c.latestTag = latest
	c.fetchedAt = time.Now()
	c.hasResult = true
	c.mu.Unlock()
	return nil
}

// highestRelease returns the highest-versioned tag among releases (by
// version.Compare, not by list order -- GitHub's own ordering isn't
// relied on) whose stage meets channel (version.MeetsChannel): a
// "release" channel skips any pre-release tag entirely; a pre-release
// channel (alpha/beta/rc) also considers tags at that stage or more
// stable, and picks the single highest across all of them, which
// naturally prefers a pre-release over any older real release. Skips
// anything that doesn't parse as this repo's tag convention rather than
// failing the whole scan.
func highestRelease(releases []release, channel string) (string, bool) {
	var best string
	found := false
	for _, r := range releases {
		ok, err := version.MeetsChannel(r.TagName, channel)
		if err != nil || !ok {
			continue
		}
		if !found {
			best, found = r.TagName, true
			continue
		}
		if c, err := version.Compare(r.TagName, best); err == nil && c > 0 {
			best = r.TagName
		}
	}
	return best, found
}

// Latest returns the cached latest real release tag, when it was
// fetched, and whether a successful fetch has ever completed.
func (c *Client) Latest() (tag string, fetchedAt time.Time, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.latestTag, c.fetchedAt, c.hasResult
}

// SetChannel changes which channel future Refresh calls consider (see
// New's channel) -- e.g. from an admin-page selector. It doesn't itself
// refresh; the caller should call Refresh afterwards so Latest()
// reflects the new channel promptly instead of waiting for the next
// timer tick. Returns an error, and leaves the current channel
// unchanged, if channel isn't one of version.Channels.
func (c *Client) SetChannel(channel string) error {
	if !version.ValidChannel(channel) {
		return fmt.Errorf("selfupdate: invalid channel %q (want one of %v)", channel, version.Channels)
	}
	c.mu.Lock()
	c.channel = channel
	c.mu.Unlock()
	return nil
}

// Channel reports the channel currently in effect.
func (c *Client) Channel() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.channel
}

// Run refreshes c on a timer until ctx is done -- once immediately, then
// every interval. onError is called (not treated as fatal) on a failed
// refresh; a self-update check failing must never take down the
// aggregator itself. onError may be nil.
func Run(ctx context.Context, c *Client, interval time.Duration, onError func(error)) {
	if err := c.Refresh(ctx); err != nil && onError != nil {
		onError(err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.Refresh(ctx); err != nil && onError != nil {
				onError(err)
			}
		}
	}
}
