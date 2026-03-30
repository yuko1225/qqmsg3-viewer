package avatar

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// unknownNick is a sentinel stored in nicknames map when we've tried but got no result,
// so we don't keep hammering the API for the same UIN.
const unknownNick = "\x00"

// Cache manages avatar images and nickname lookups with rate limiting.
type Cache struct {
	dir       string
	rateLimit float64 // requests per second
	mu        sync.Mutex
	pending   map[uint64]bool
	ticker    *time.Ticker
	queue     chan fetchReq
	done      chan struct{}
	nicknames map[uint64]string
	nickMu    sync.RWMutex
}

// fetchReq is a request to fetch avatar for a UIN.
// isGroup is kept for potential future use but group avatars are now SVG-only.
type fetchReq struct {
	uin     uint64
	isGroup bool
}

// NewCache creates a new Cache.
func NewCache(dir string, rateLimit float64) (*Cache, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	if rateLimit <= 0 {
		rateLimit = 1.0
	}
	interval := time.Duration(float64(time.Second) / rateLimit)
	c := &Cache{
		dir:       dir,
		rateLimit: rateLimit,
		pending:   make(map[uint64]bool),
		queue:     make(chan fetchReq, 512),
		done:      make(chan struct{}),
		nicknames: make(map[uint64]string),
		ticker:    time.NewTicker(interval),
	}
	c.loadNicknames()
	go c.worker()
	return c, nil
}

// Close stops the background worker.
func (c *Cache) Close() {
	close(c.done)
	c.ticker.Stop()
}

// AvatarPath returns the local file path for a QQ avatar.
// If the file doesn't exist yet, it enqueues a download and returns "".
func (c *Cache) AvatarPath(uin uint64) string {
	p := c.localPath(uin)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	c.enqueue(uin, false)
	return ""
}

// Nickname returns the cached nickname for a UIN, or "" if not yet known.
func (c *Cache) Nickname(uin uint64) string {
	c.nickMu.RLock()
	defer c.nickMu.RUnlock()
	v := c.nicknames[uin]
	if v == unknownNick {
		return ""
	}
	return v
}

// NicknameFetched returns true if we have already attempted to fetch the nickname
// for this UIN (regardless of whether the result was empty or not).
// NOTE: Since the QQ nickname API is currently unavailable, this always returns
// true after the first attempt so the frontend stops polling.
func (c *Cache) NicknameFetched(uin uint64) bool {
	c.nickMu.RLock()
	defer c.nickMu.RUnlock()
	_, ok := c.nicknames[uin]
	return ok
}

// Prefetch enqueues a list of UINs for background avatar fetching.
func (c *Cache) Prefetch(uins []uint64) {
	for _, u := range uins {
		c.enqueue(u, false)
	}
}

// PrefetchGroup is a no-op: group avatars are now rendered as SVG in the frontend.
func (c *Cache) PrefetchGroup(groupUin uint64) {
	// Group avatars are displayed as SVG default icons; no API call needed.
}

func (c *Cache) enqueue(uin uint64, isGroup bool) {
	if isGroup {
		return // group avatars not fetched via API
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending[uin] {
		return
	}
	// Already have avatar on disk?
	if _, err := os.Stat(c.localPath(uin)); err == nil {
		// Also mark nickname as resolved (sentinel) so frontend stops polling
		c.nickMu.Lock()
		if _, ok := c.nicknames[uin]; !ok {
			c.nicknames[uin] = unknownNick
		}
		c.nickMu.Unlock()
		return
	}

	c.pending[uin] = true
	req := fetchReq{uin: uin, isGroup: false}
	select {
	case c.queue <- req:
	default:
		delete(c.pending, uin)
	}
}

func (c *Cache) worker() {
	for {
		select {
		case <-c.done:
			return
		case req := <-c.queue:
			select {
			case <-c.ticker.C:
			case <-c.done:
				return
			}
			c.fetchOne(req)
			c.mu.Lock()
			delete(c.pending, req.uin)
			c.mu.Unlock()
		}
	}
}

// fetchOne downloads avatar for a UIN.
// Nickname fetching is disabled because the QQ public nickname API is no longer available.
func (c *Cache) fetchOne(req fetchReq) {
	uin := req.uin
	uinStr := strconv.FormatUint(uin, 10)

	// Mark nickname as "resolved" (sentinel) immediately so frontend stops polling.
	// If a working nickname API becomes available in the future, this can be re-enabled.
	c.nickMu.Lock()
	if _, ok := c.nicknames[uin]; !ok {
		c.nicknames[uin] = unknownNick
	}
	c.nickMu.Unlock()

	// Fetch personal avatar image (only if not yet cached)
	if _, err := os.Stat(c.localPath(uin)); err == nil {
		return
	}
	avatarURL := fmt.Sprintf("https://q1.qlogo.cn/g?b=qq&nk=%s&s=100", uinStr)
	c.downloadAvatar(uin, avatarURL)
}

// downloadAvatar fetches an image from url and saves it to the local cache.
func (c *Cache) downloadAvatar(uin uint64, avatarURL string) {
	resp, err := httpGet(avatarURL)
	if err != nil {
		log.Printf("[avatar] fetch avatar for %d: %v", uin, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Printf("[avatar] avatar HTTP %d for %d", resp.StatusCode, uin)
		return
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		log.Printf("[avatar] read avatar for %d: %v", uin, err)
		return
	}

	p := c.localPath(uin)
	if err := os.WriteFile(p, data, 0644); err != nil {
		log.Printf("[avatar] write avatar for %d: %v", uin, err)
	}
}

func (c *Cache) localPath(uin uint64) string {
	return filepath.Join(c.dir, fmt.Sprintf("%d.jpg", uin))
}

func (c *Cache) nicknamesFile() string {
	return filepath.Join(c.dir, "nicknames.json")
}

func (c *Cache) loadNicknames() {
	data, err := os.ReadFile(c.nicknamesFile())
	if err != nil {
		return
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	c.nickMu.Lock()
	defer c.nickMu.Unlock()
	for k, v := range m {
		var u uint64
		fmt.Sscanf(k, "%d", &u)
		c.nicknames[u] = v
	}
}

func (c *Cache) saveNicknames() {
	c.nickMu.RLock()
	m := make(map[string]string, len(c.nicknames))
	for k, v := range c.nicknames {
		if v != unknownNick {
			m[strconv.FormatUint(k, 10)] = v
		}
	}
	c.nickMu.RUnlock()

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(c.nicknamesFile(), data, 0644)
}

// ServeAvatar writes the avatar image for a UIN to w.
// Returns false if not yet cached (triggers background download).
func (c *Cache) ServeAvatar(uin uint64, w http.ResponseWriter) bool {
	p := c.localPath(uin)
	data, err := os.ReadFile(p)
	if err != nil {
		c.enqueue(uin, false)
		return false
	}
	// Detect content type (QQ returns PNG or JPEG)
	ct := http.DetectContentType(data)
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(data)
	return true
}

// ServeGroupAvatar is a no-op: group avatars are rendered as SVG in the frontend.
// Returns false so the handler serves the default SVG.
func (c *Cache) ServeGroupAvatar(uin uint64, w http.ResponseWriter) bool {
	return false
}

// AvatarBytes returns the raw avatar image bytes for a UIN from the local cache.
// Returns nil if the avatar is not yet cached (does NOT trigger a download).
func (c *Cache) AvatarBytes(uin uint64) []byte {
	p := c.localPath(uin)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	return data
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

func httpGet(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	return httpClient.Do(req)
}
