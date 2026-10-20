package routerredact

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"sync"
	"time"
)

type Stats struct {
	Processed  uint64            `json:"processed"`
	Entities   uint64            `json:"entities"`
	CacheHits  uint64            `json:"cacheHits"`
	Blocked    uint64            `json:"blocked"`
	DurationMS int64             `json:"durationMs"`
	Failures   map[string]uint64 `json:"failures"`
}
type cacheKey struct {
	app, session string
	protocol     Protocol
	operation    string
	versions     Versions
}
type cacheEntry struct {
	fingerprints        [][32]byte
	processed           []string
	mapping, categories map[string]string
	expires, used       time.Time
	bytes               int
}
type Pipeline struct {
	mu        sync.Mutex
	options   Options
	cache     map[cacheKey]cacheEntry
	bytes     int
	epoch     uint64
	closed    bool
	stats     Stats
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}
type Prepared struct {
	*Snapshot
	mu   sync.RWMutex
	body []byte
}

func (p *Prepared) Body() ([]byte, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.body == nil {
		return nil, fail("redaction_released")
	}
	return append([]byte(nil), p.body...), nil
}
func (p *Prepared) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.body = nil
	p.mu.Unlock()
	p.Snapshot.Close()
}
func New(options Options) *Pipeline {
	maximumBudget := 15 * time.Second
	if options.Experimental {
		maximumBudget = 30 * time.Second
	}
	if options.Budget <= 0 || options.Budget > maximumBudget {
		options.Budget = maximumBudget
	}
	if options.CacheEntries <= 0 {
		options.CacheEntries = 128
	}
	if options.CacheBytes <= 0 {
		options.CacheBytes = 16 << 20
	}
	if options.CacheTTL <= 0 {
		options.CacheTTL = 15 * time.Minute
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	p := &Pipeline{options: options, cache: map[cacheKey]cacheEntry{}, stats: Stats{Failures: map[string]uint64{}}, stop: make(chan struct{}), done: make(chan struct{})}
	go p.expiryLoop()
	return p
}
func cloneStrings(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Prepare has no outbound transport. Every caller must stop on its error.
// The injected extractor must honor ctx and must not retain source text.
func (p *Pipeline) Prepare(parent context.Context, in Input) (prepared *Prepared, err error) {
	started := time.Now()
	hit := false
	entityCount := 0
	defer func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.stats.Processed++
		p.stats.DurationMS += time.Since(started).Milliseconds()
		if hit {
			p.stats.CacheHits++
		}
		if err != nil {
			p.stats.Blocked++
			var f *Failure
			if !errors.As(err, &f) {
				f = &Failure{Code: "redaction_failed"}
			}
			p.stats.Failures[SafeFailureCode(f.Code)]++
		} else {
			p.stats.Entities += uint64(entityCount)
		}
	}()
	ctx, cancel := context.WithTimeout(parent, p.options.Budget)
	defer cancel()
	if err = contextError(ctx); err != nil {
		return nil, err
	}
	if in.Operation == "generate" {
		in.Operation = "generation"
	}
	if in.RequestID == "" || len(in.RequestID) > 128 || in.AppID == "" || len(in.AppID) > 48 || len(in.SessionID) > 256 || len(in.SeedSessionID) > 256 || in.Versions.Model == "" || len(in.Versions.Model) > 256 || in.Versions.Prompt == "" || len(in.Versions.Prompt) > 256 || in.Versions.Config == "" || len(in.Versions.Config) > 256 || (in.Operation != "generation" && in.Operation != "count_tokens") || len(in.Body) > 8<<20 {
		return nil, fail("redaction_invalid_input")
	}
	if p.options.Extractor == nil {
		return nil, fail("redaction_unavailable")
	}
	if available, ok := p.options.Extractor.(interface {
		Available(context.Context, Versions) error
	}); ok {
		if checkErr := available.Available(ctx, in.Versions); checkErr != nil {
			if err = contextError(ctx); err != nil {
				return nil, err
			}
			return nil, fail("redaction_unavailable")
		}
	}
	body := append([]byte(nil), in.Body...)
	doc, err := parseRequest(in.Protocol, body)
	if err != nil {
		return nil, err
	}
	if len(doc.texts) > 8192 {
		return nil, fail("redaction_input_limit")
	}
	fingerprints := make([][32]byte, len(doc.texts))
	textBytes := 0
	for i, text := range doc.texts {
		textBytes += len(text)
		fingerprints[i] = sha256.Sum256([]byte(doc.identities[i] + "\x00" + text))
	}
	if textBytes > 8<<20 {
		return nil, fail("redaction_input_limit")
	}
	key := cacheKey{in.AppID, in.SessionID, in.Protocol, in.Operation, in.Versions}
	cacheable := in.SessionVerified && in.SessionID != ""
	mapping, categories := map[string]string{}, map[string]string{}
	processed := []string{}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, fail("redaction_closed")
	}
	epoch := p.epoch
	now := p.options.Now()
	p.expireLocked(now)
	if cached, ok := p.cache[key]; ok && cacheable && len(cached.fingerprints) <= len(fingerprints) {
		equal := true
		for i, fingerprint := range cached.fingerprints {
			if fingerprint != fingerprints[i] {
				equal = false
				break
			}
		}
		// A later literal equal to a prior generated token must never be restored
		// as the prior entity. Re-scan and allocate a fresh collision-free mapping.
		if equal {
			for _, text := range doc.texts {
				for token := range cached.mapping {
					if strings.Contains(text, token) {
						equal = false
						break
					}
				}
				if !equal {
					break
				}
			}
		}
		if equal {
			mapping = cloneStrings(cached.mapping)
			categories = cloneStrings(cached.categories)
			processed = append(processed, cached.processed...)
			hit = true
			cached.used = now
			p.cache[key] = cached
		}
	}
	p.mu.Unlock()
	increment := append([]string(nil), doc.texts[len(processed):]...)
	if len(increment) > 0 || !hit {
		result, extractErr := p.options.Extractor.Extract(ctx, Extraction{Versions: in.Versions, Texts: append([]string(nil), increment...)})
		if err = contextError(ctx); err != nil {
			return nil, err
		}
		if extractErr != nil {
			return nil, fail("redaction_extraction_failed")
		}
		entities, validationErr := validateEntities(increment, result)
		if validationErr != nil {
			return nil, validationErr
		}
		entityCount = len(entities)
		replaced, replaceErr := applyEntities(increment, entities, mapping, categories, doc.texts)
		if replaceErr != nil {
			return nil, replaceErr
		}
		processed = append(processed, replaced...)
	}
	if err = contextError(ctx); err != nil {
		return nil, err
	}
	rendered, err := doc.render(processed)
	if err != nil {
		return nil, err
	}
	if err = contextError(ctx); err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, fail("redaction_closed")
	}
	writeKey := key
	canWrite := cacheable
	if !canWrite && in.SeedSessionID != "" {
		writeKey = cacheKey{in.AppID, in.SeedSessionID, in.Protocol, in.Operation, in.Versions}
		canWrite = true
	}
	if canWrite && epoch == p.epoch {
		entry := cacheEntry{fingerprints, append([]string(nil), processed...), cloneStrings(mapping), cloneStrings(categories), now.Add(p.options.CacheTTL), now, 0}
		for _, s := range entry.processed {
			entry.bytes += len(s)
		}
		entry.bytes += len(fingerprints) * 32
		for k, v := range mapping {
			entry.bytes += len(k) + len(v) + len(categories[k])
		}
		p.putLocked(writeKey, entry)
	}
	p.mu.Unlock()
	return &Prepared{Snapshot: &Snapshot{mapping: mapping}, body: rendered}, nil
}
func contextError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return fail("redaction_canceled")
	}
	if ctx.Err() != nil {
		return fail("redaction_timeout")
	}
	return nil
}
func (p *Pipeline) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.stats
	out.Failures = make(map[string]uint64, len(p.stats.Failures))
	for k, v := range p.stats.Failures {
		out.Failures[k] = v
	}
	return out
}

// Clear invalidates even in-progress cache publishers. Existing snapshots live
// until their owner closes them. Close additionally stops new admissions.
func (p *Pipeline) Clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cache = map[cacheKey]cacheEntry{}
	p.bytes = 0
	p.epoch++
}
func (p *Pipeline) Close() {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.cache = nil
		p.bytes = 0
		p.epoch++
		close(p.stop)
		p.mu.Unlock()
	})
	<-p.done
}

// Sweep is also exposed for injected-clock tests and explicit maintenance.
func (p *Pipeline) Sweep() { p.mu.Lock(); defer p.mu.Unlock(); p.expireLocked(p.options.Now()) }
func (p *Pipeline) expiryLoop() {
	defer close(p.done)
	timer := time.NewTicker(min(p.options.CacheTTL, time.Minute))
	defer timer.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-timer.C:
			p.Sweep()
		}
	}
}
func (p *Pipeline) expireLocked(now time.Time) {
	for k, v := range p.cache {
		if !now.Before(v.expires) {
			delete(p.cache, k)
			p.bytes -= v.bytes
		}
	}
}
func (p *Pipeline) putLocked(key cacheKey, entry cacheEntry) {
	if old, ok := p.cache[key]; ok {
		p.bytes -= old.bytes
		delete(p.cache, key)
	}
	if entry.bytes > p.options.CacheBytes {
		return
	}
	for len(p.cache) >= p.options.CacheEntries || p.bytes+entry.bytes > p.options.CacheBytes {
		var oldest cacheKey
		var when time.Time
		for k, v := range p.cache {
			if when.IsZero() || v.used.Before(when) {
				oldest = k
				when = v.used
			}
		}
		p.bytes -= p.cache[oldest].bytes
		delete(p.cache, oldest)
	}
	p.cache[key] = entry
	p.bytes += entry.bytes
}
