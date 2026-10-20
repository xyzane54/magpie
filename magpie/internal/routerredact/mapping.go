package routerredact

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

const maxEntities = 4096
const maxEntityBytes = 4096
const maxPlaceholderBytes = 40

// Snapshot is private to one request. Closing it drops its sensitive references.
// Callers must join response restoration before Close. It never persists data.
type Snapshot struct {
	mu      sync.RWMutex
	mapping map[string]string
	closed  bool
}

func (s *Snapshot) mappingCopy() (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, fail("redaction_released")
	}
	out := make(map[string]string, len(s.mapping))
	for k, v := range s.mapping {
		out[k] = v
	}
	return out, nil
}

// RestoreText does a single pass: restored originals are never interpreted again.
func (s *Snapshot) RestoreText(text string) (string, error) {
	if s == nil {
		return "", fail("redaction_released")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return "", fail("redaction_released")
	}
	if !utf8.ValidString(text) {
		return "", fail("redaction_invalid_response")
	}
	if len(text) > 32<<20 {
		return "", fail("redaction_response_limit")
	}
	var out strings.Builder
	for i := 0; i < len(text); {
		if text[i] == '<' {
			end := strings.IndexByte(text[i:min(len(text), i+maxPlaceholderBytes)], '>')
			if end >= 0 {
				token := text[i : i+end+1]
				if value, ok := s.mapping[token]; ok {
					if out.Len()+len(value) > 32<<20 {
						return "", fail("redaction_response_limit")
					}
					out.WriteString(value)
					i += end + 1
					continue
				}
			}
		}
		if out.Len() >= 32<<20 {
			return "", fail("redaction_response_limit")
		}
		out.WriteByte(text[i])
		i++
	}
	return out.String(), nil
}

func (s *Snapshot) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.mapping = nil
	s.closed = true
	s.mu.Unlock()
}

func categoryValid(c string) bool {
	if len(c) < 1 || len(c) > 32 {
		return false
	}
	for i, v := range c {
		if i == 0 {
			if v < 'A' || v > 'Z' {
				return false
			}
			continue
		}
		if (v < 'A' || v > 'Z') && (v < '0' || v > '9') && v != '_' {
			return false
		}
	}
	return true
}

func validateEntities(texts []string, r Result) ([]Entity, error) {
	if !r.Complete || len(r.Entities) > maxEntities {
		return nil, fail("redaction_invalid_result")
	}
	entities := append([]Entity(nil), r.Entities...)
	sort.Slice(entities, func(i, j int) bool {
		a, b := entities[i], entities[j]
		if a.Field != b.Field {
			return a.Field < b.Field
		}
		if a.Start != b.Start {
			return a.Start < b.Start
		}
		return a.End < b.End
	})
	out := make([]Entity, 0, len(entities))
	for _, e := range entities {
		if e.Field < 0 || e.Field >= len(texts) || !categoryValid(e.Category) || e.Start < 0 || e.End <= e.Start || e.End > len(texts[e.Field]) || len(e.Text) > maxEntityBytes || e.Text == "" {
			return nil, fail("redaction_invalid_result")
		}
		text := texts[e.Field]
		if text[e.Start:e.End] != e.Text || !utf8.ValidString(e.Text) || (e.Start > 0 && !utf8.RuneStart(text[e.Start])) || (e.End < len(text) && !utf8.RuneStart(text[e.End])) {
			return nil, fail("redaction_invalid_result")
		}
		if len(out) > 0 {
			previous := out[len(out)-1]
			if previous.Field == e.Field && e.Start < previous.End {
				if previous == e {
					continue
				}
				return nil, fail("redaction_entity_conflict")
			}
		}
		out = append(out, e)
	}
	return out, nil
}

func applyEntities(texts []string, entities []Entity, mapping map[string]string, categories map[string]string, reserved []string) ([]string, error) {
	byEntity := make(map[string]string, len(mapping))
	next := map[string]int{}
	for token, value := range mapping {
		byEntity[PlaceholderKey(categories[token], value)] = token
	}
	generalizedByIndex := make(map[int]string)
	for idx, e := range entities {
		if e.Category == "AMOUNT" {
			if gen, ok := GeneralizeAmountValue(e.Text); ok {
				generalizedByIndex[idx] = gen
				continue
			}
		}
		key := PlaceholderKey(e.Category, e.Text)
		if _, ok := byEntity[key]; ok {
			continue
		}
		for {
			next[e.Category]++
			if next[e.Category] > maxEntities*4 {
				return nil, fail("redaction_placeholder_limit")
			}
			token := "<" + e.Category + ":" + strconv.Itoa(next[e.Category]) + ">"
			if _, ok := mapping[token]; ok {
				continue
			}
			collision := false
			for _, text := range reserved {
				if strings.Contains(text, token) {
					collision = true
					break
				}
			}
			if collision {
				continue
			}
			if len(mapping) >= maxEntities {
				return nil, fail("redaction_mapping_limit")
			}
			// Extractors may return substrings of a much larger source value.
			// Retain only the entity/category bytes in long-lived snapshots.
			mapping[token] = strings.Clone(e.Text)
			categories[token] = strings.Clone(e.Category)
			byEntity[key] = token
			break
		}
	}
	out := append([]string(nil), texts...)
	at := 0
	for field, text := range texts {
		var b strings.Builder
		cursor := 0
		for at < len(entities) && entities[at].Field == field {
			e := entities[at]
			b.WriteString(text[cursor:e.Start])
			if gen, ok := generalizedByIndex[at]; ok {
				b.WriteString(gen)
			} else {
				b.WriteString(byEntity[PlaceholderKey(e.Category, e.Text)])
			}
			cursor = e.End
			at++
		}
		b.WriteString(text[cursor:])
		out[field] = b.String()
	}
	return out, nil
}
