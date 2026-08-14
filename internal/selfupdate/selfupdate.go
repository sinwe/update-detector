// Package selfupdate checks a GitHub repo's release list for the
// newest real (non-pre-release) tag of update-detector itself, so the
// aggregator can tell a fleet "vX.Y.Z is available."
package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"update-detector/internal/version"
)

const defaultGitHubAPI = "https://api.github.com/repos/winarto/update-detector"

type release struct {
	TagName string `json:"tag_name"`
	// GitHub returns "prerelease": true for pre-releases, but we also
	// filter by tag name convention (version.IsPreRelease) for consistency
	// with our own tagging scheme.
	PreRelease bool `json:"prerelease"`
}

// Client checks a GitHub repo's release list for the newest release
// tag matching the configured channel, caching the result in memory.
// Safe for concurrent use.
type Client struct {
	apiBase string
	http    *http.Client

	mu                sync.Mutex
	includePreRelease bool // mutable via SetIncludePreRelease, e.g. from an admin-page toggle
	latestTag         string
	fetchedAt         time.Time
	hasResult         bool

	// subscribers are notified (in their own goroutines) whenever a
	// Refresh changes the cached latestTag. Nil-safe: OnChange is
	// only called when the set of subscribers is non-empty.
	subscribers map[int64]func(latestTag string)
	nextSubID   int64
}

// New returns a Client for apiBase (e.g.
// "https://api.github.com/repos/winarto/update-detector"), or
// this repo's own default if apiBase is empty. includePreRelease
// selects the channel: false considers only real releases (the default,
// safer choice for a production fleet); true also considers -rcN tags,
// and picks the single highest tag across both real releases and
// pre-releases -- so an available -rc *always* outranks an older real
// release, on the assumption that anyone opting into this channel wants
// the newest build available, pre-release or not.
func New(apiBase string, includePreRelease bool) *Client {
	if apiBase == "" {
		apiBase = defaultGitHubAPI
	}
	return &Client{
		apiBase:           apiBase,
		includePreRelease: includePreRelease,
		http: &http.Client{
			Timeout: 15 * time.Second,
		},
		subscribers: make(map[int64]func(latestTag string)),
	}
}

// Refresh fetches the release list and updates the cached latest real
// release tag. A fetch failure (network, non-200, bad JSON, or no real
// release found at all) leaves any previously cached result untouched --
// a transient GitHub/network outage must not erase an otherwise-valid
// "update available" fact the admin page is showing.
//
// Deliberately not GET /releases/latest: this repo's own tags (see
// .github/workflows/release.yml's `tags: - 'v*'` trigger) include -rcN
// pre-releases, and the release-creation call there never sets a
// "prerelease" flag -- so GitHub's own "latest" can be an rc build if
// it happens to be the most recently published release. Fetching the
// list and filtering by tag-name convention (version.IsPreRelease) is
// the only reliable way to exclude those.
//
// Fetches one page with a generous limit rather than paginating --
// missing the true latest release would require more pre-release-only
// tags pushed after it than the limit, which isn't a realistic release
// cadence for this repo; a deliberate, accepted simplification.
func (c *Client) Refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase+"/releases?per_page=20", nil)
	if err != nil {
		return fmt.Errorf("selfupdate: building request: %w", err)
	}

	// Add GitHub token if available for higher rate limits
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
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

	latest, ok := highestRelease(releases, c.IncludePreRelease())
	if !ok {
		return fmt.Errorf("selfupdate: no matching release found")
	}

	c.mu.Lock()
	prevTag := c.latestTag
	c.latestTag = latest
	c.fetchedAt = time.Now()
	c.hasResult = true
	subs := c.subscribers
	c.mu.Unlock()

	// Notify subscribers only when the latest tag actually changed.
	if latest != prevTag && len(subs) > 0 {
		for _, fn := range subs {
			go fn(latest)
		}
	}
	return nil
}

// highestRelease returns the highest-versioned tag among releases (by
// version.Compare, not by list order -- GitHub's own ordering isn't
// relied on) matching the given channel: includePreRelease=false skips
// any -rcN tag entirely; true considers both and picks the single
// highest across both, which naturally prefers a pre-release over any
// older real release. Skips anything that doesn't parse as this repo's
// tag convention rather than failing the whole scan.
func highestRelease(releases []release, includePreRelease bool) (string, bool) {
	var best string
	found := false
	for _, r := range releases {
		// Skip GitHub-marked pre-releases if we're not including them
		if !includePreRelease && r.PreRelease {
			continue
		}
		// Also skip by our own tag convention (e.g., -rcN)
		if !includePreRelease && version.IsPreRelease(r.TagName) {
			continue
		}
		if !found {
			// Validate parseability before accepting as the initial
			// candidate -- IsPreRelease returns false (not an error)
			// for anything unparseable, so an unrelated tag would
			// otherwise silently become "best" just for being first,
			// and every real comparison against it would then fail to
			// parse and be skipped by the branch below.
			if _, err := version.Compare(r.TagName, r.TagName); err != nil {
				continue
			}
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

// SetIncludePreRelease changes which channel future Refresh calls
// consider (see New's includePreRelease) -- e.g. from an admin-page
// toggle. It doesn't itself refresh; the caller should call Refresh
// afterwards so Latest() reflects the new channel promptly instead of
// waiting for the next timer tick.
func (c *Client) SetIncludePreRelease(v bool) {
	c.mu.Lock()
	c.includePreRelease = v
	c.mu.Unlock()
}

// IncludePreRelease reports the channel currently in effect.
func (c *Client) IncludePreRelease() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.includePreRelease
}

// OnChange registers fn to be called (in its own goroutine) whenever a
// successful Refresh changes the cached latest tag. The returned
// function unregisters fn. This is used by the aggregator's SSE endpoint
// to push version updates to connected browsers without polling.
func (c *Client) OnChange(fn func(latestTag string)) (unsubscribe func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextSubID
	c.nextSubID++
	c.subscribers[id] = fn
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.subscribers, id)
	}
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
