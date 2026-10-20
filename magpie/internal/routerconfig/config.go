// Package routerconfig stores API-router's configuration independently of
// Magpie settings, accounts, plugins and migrations.
package routerconfig

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/steady"
)

// SchemaVersion applies to config.json, credentials.json and models/manifest.json.
// App/provider and model contents are owned by W1 and T3 respectively.
const SchemaVersion = 1

// Document retains unknown fields, including nested values, across edits.
// Credentials belong in credentials.json; config holds only their reference IDs.
type Document map[string]json.RawMessage

var sections = []string{"listen", "apps", "providers", "redaction", "runtime"}

func defaults() (Document, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	b, err := json.Marshal(map[string]any{
		"schemaVersion": SchemaVersion, "instanceId": hex.EncodeToString(id[:]),
		"listen": map[string]any{"address": "127.0.0.1:13145"},
		"apps":   map[string]any{}, "providers": map[string]any{},
		"redaction": map[string]any{"enabled": false}, "runtime": map[string]any{},
	})
	if err != nil {
		return nil, err
	}
	return parse(b)
}

// Config represents the decoded router configuration view, including providers and model groups.
type Config struct {
	SchemaVersion int             `json:"schemaVersion,omitempty"`
	InstanceID    string          `json:"instanceId,omitempty"`
	Providers     []CloudProvider `json:"providers,omitempty"`
	Groups        []Group         `json:"groups,omitempty"`
	GroupOrder    []string        `json:"groupOrder,omitempty"`
	NoAutoGroups  bool            `json:"noAutoGroups,omitempty"`
}

func (c *Config) UnmarshalJSON(b []byte) error {
	var raw struct {
		SchemaVersion int             `json:"schemaVersion"`
		InstanceID    string          `json:"instanceId"`
		Providers     json.RawMessage `json:"providers"`
		Groups        []Group         `json:"groups"`
		GroupOrder    []string        `json:"groupOrder"`
		NoAutoGroups  bool            `json:"noAutoGroups"`
	}
	if err := json.Unmarshal(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), &raw); err != nil {
		return err
	}
	*c = Config{SchemaVersion: raw.SchemaVersion, InstanceID: raw.InstanceID, Groups: raw.Groups, GroupOrder: raw.GroupOrder, NoAutoGroups: raw.NoAutoGroups}
	trimmed := bytes.TrimSpace(raw.Providers)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] == '[' {
		return json.Unmarshal(trimmed, &c.Providers)
	}
	var m map[string]CloudProvider
	if err := json.Unmarshal(trimmed, &m); err != nil {
		return err
	}
	c.Providers = decodeProvidersMap(trimmed)
	return nil
}

func parse(b []byte) (Document, error) {
	var d Document
	if err := json.Unmarshal(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), &d); err != nil {
		return nil, err
	}
	var version int
	if err := json.Unmarshal(d["schemaVersion"], &version); err != nil || version != SchemaVersion {
		return nil, fmt.Errorf("unsupported or missing schemaVersion (requires %d)", SchemaVersion)
	}
	var id string
	if err := json.Unmarshal(d["instanceId"], &id); err != nil || id == "" {
		return nil, errors.New("instanceId must be a nonempty string")
	}
	for _, name := range sections {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(d[name], &object); err != nil || object == nil {
			return nil, fmt.Errorf("%s must be a JSON object", name)
		}
	}
	if _, err := readApps(d); err != nil {
		return nil, err
	}
	var runtime struct {
		Settings RuntimeSettings `json:"settings"`
	}
	if err := json.Unmarshal(d["runtime"], &runtime); err != nil {
		return nil, err
	}
	if err := runtime.Settings.Validate(); err != nil {
		return nil, fmt.Errorf("runtime settings: %w", err)
	}
	if rawGroups, ok := d["groups"]; ok && len(bytes.TrimSpace(rawGroups)) > 0 && !bytes.Equal(bytes.TrimSpace(rawGroups), []byte("null")) {
		var groups []Group
		if err := json.Unmarshal(rawGroups, &groups); err != nil {
			return nil, fmt.Errorf("groups must be a JSON array: %w", err)
		}
		if _, err := ValidateAndNormalizeGroups(groups); err != nil {
			return nil, fmt.Errorf("groups: %w", err)
		}
	}
	if rawOrder, ok := d["groupOrder"]; ok && len(bytes.TrimSpace(rawOrder)) > 0 && !bytes.Equal(bytes.TrimSpace(rawOrder), []byte("null")) {
		var order []string
		if err := json.Unmarshal(rawOrder, &order); err != nil {
			return nil, fmt.Errorf("groupOrder must be a JSON string array: %w", err)
		}
	}
	return d, nil
}

// Store owns config.json. Separate processes serialize transactions with an exclusive
// lock directory. A leftover lock after a crash requires explicit removal after
// checking no writer is running; it is never guessed stale and taken over.
type Store struct{ layout dirs.Layout }

func New(layout dirs.Layout) *Store { return &Store{layout: layout} }

// Load never creates defaults. Only a missing file returns fs.ErrNotExist;
// unreadable, empty, malformed and incompatible files are errors.
func (s *Store) Load() (Document, error) {
	return s.LoadContext(context.Background())
}

func (s *Store) LoadContext(ctx context.Context) (Document, error) {
	ctx, unlock, err := s.readLockContext(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return s.load(ctx)
}

// load is called only while the transaction lock is held.
func (s *Store) load(ctx context.Context) (Document, error) {
	if err := s.regularFile(); err != nil {
		return nil, err
	}
	b, err := steady.ReadFileContext(ctx, s.layout.ConfigFile())
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.layout.ConfigFile(), err)
	}
	d, err := parse(b)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", s.layout.ConfigFile(), err)
	}
	return d, ctx.Err()
}

func (s *Store) regularFile() error {
	p := s.layout.ConfigFile()
	fi, err := os.Lstat(p)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", p, err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("config must be a regular file: %s", p)
	}
	return nil
}

// Initialize creates defaults only when config.json is absent. Repeating it
// preserves the existing bytes and instance ID, and rejects a damaged file.
func (s *Store) Initialize() (Document, error) {
	return s.InitializeContext(context.Background())
}

func (s *Store) InitializeContext(ctx context.Context) (Document, error) {
	ctx, unlock, err := s.lockContext(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	d, err := s.load(ctx)
	if err == nil {
		return d, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	d, err = defaults()
	if err != nil {
		return nil, err
	}
	if err := s.write(ctx, d); err != nil {
		return nil, err
	}
	return d, nil
}

// Update edits the current document under the writer lock. Unknown fields stay
// in the document. A failed read or edit leaves both config and backup intact.
// The previous valid bytes are backed up before a flushed atomic replacement.
func (s *Store) Update(edit func(Document) error) error {
	return s.UpdateContext(context.Background(), edit)
}

func (s *Store) UpdateContext(ctx context.Context, edit func(Document) error) error {
	ctx, unlock, err := s.lockContext(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.regularFile(); err != nil {
		return err
	}
	b, err := steady.ReadFileContext(ctx, s.layout.ConfigFile())
	if err != nil {
		return err
	}
	d, err := parse(b)
	if err != nil {
		return err
	}
	if err := edit(d); err != nil {
		return err
	}
	// Validate before writing either file.
	if _, err := encode(d); err != nil {
		return err
	}
	if err := steady.WriteFileContext(ctx, s.layout.ConfigFile()+".bak", b, 0o600); err != nil {
		return err
	}
	return s.write(ctx, d)
}

func encode(d Document) ([]byte, error) {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return nil, err
	}
	if _, err := parse(b); err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func (s *Store) write(ctx context.Context, d Document) error {
	b, err := encode(d)
	if err != nil {
		return err
	}
	return steady.WriteFileContext(ctx, s.layout.ConfigFile(), b, 0o600)
}
