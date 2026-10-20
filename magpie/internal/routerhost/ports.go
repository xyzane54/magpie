package routerhost

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/yetone/magpie/internal/edit"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// PortTarget identifies an already connected host. Port synchronization never
// creates a provider or changes its model, credentials, or selection.
type PortTarget struct {
	Host, ConfigRoot, Model string
}

type portGroup struct {
	Host, Root string
	Files      []snapshot
	Journal    snapshot
}

type portTransaction struct {
	Version   int
	Groups    []portGroup
	LockToken string
	Committed bool
	Checksum  string
}

const portTransactionFile = "port-sync.json"

func (e Engine) portLockPaths(groups []portGroup) []string {
	paths := []string{filepath.Join(e.StateDir, ".operation.lock")}
	seen := map[string]bool{}
	for _, group := range groups {
		path := filepath.Join(group.Root, ".api-router-host.lock")
		if !seen[path] {
			paths = append(paths, path)
			seen[path] = true
		}
	}
	return paths
}

// The service lock excludes an active startup. Only directories bearing this
// exact pending transaction's marker can be recovered after a process crash.
func (e Engine) recoverPortLocks(pending portTransaction) error {
	var owned []string
	for _, path := range e.portLockPaths(pending.Groups) {
		entries, err := os.ReadDir(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err := validateRoot(path); err != nil {
			return err
		}
		if len(entries) != 1 || entries[0].Name() != "port-sync-owner" {
			return errors.New("unknown host operation lock blocks port synchronization recovery")
		}
		marker, err := e.readSnapshot(path, FilePlan{Name: "port-sync-owner"})
		if err != nil || string(marker.Before) != pending.LockToken {
			return errors.New("another operation owns the host lock; no lock changed")
		}
		owned = append(owned, path)
	}
	for _, path := range owned {
		current, err := e.readSnapshot(path, FilePlan{Name: "port-sync-owner"})
		if err != nil || string(current.Before) != pending.LockToken {
			return errors.New("host operation lock changed during recovery")
		}
		marker := filepath.Join(path, "port-sync-owner")
		if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func validPortOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	host, port, err := net.SplitHostPort(u.Host)
	n, portErr := strconv.Atoi(port)
	ip := net.ParseIP(host)
	return err == nil && portErr == nil && n > 0 && n <= 65535 && ip != nil && ip.IsLoopback()
}

func (p portTransaction) checksum() string {
	p.Checksum = ""
	b, _ := json.Marshal(p)
	return sum(b)
}

func portAdapter(host string) (Adapter, error) {
	switch host {
	case "workbuddy":
		return WorkBuddyAdapter{}, nil
	case "loomy":
		return LoomyAdapter{}, nil
	default:
		return nil, errors.New("port synchronization supports WorkBuddy and Loomy only")
	}
}

// ConnectedRoots includes explicit/custom directories from validated recovery
// records, so an installed host need not live in its default directory.
func ConnectedRoots(stateDir, host string) ([]string, error) {
	if _, err := portAdapter(host); err != nil {
		return nil, err
	}
	if err := validateRoot(stateDir); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(stateDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var roots []string
	e := Engine{StateDir: stateDir}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), host+"-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if len(roots) >= 32 {
			return nil, errors.New("too many connected host directories")
		}
		s, err := e.readSnapshot(stateDir, FilePlan{Name: entry.Name()})
		if err != nil {
			return nil, err
		}
		var record journal
		if json.Unmarshal(s.Before, &record) != nil || filepath.Base(e.recordPath(host, record.Root)) != entry.Name() {
			return nil, errors.New("invalid host recovery record during port synchronization")
		}
		if err := e.validateState(record.Root); err != nil {
			return nil, err
		}
		j, _, err := e.readJournal(host, record.Root)
		if err != nil {
			return nil, err
		}
		if j.Status != "restored" {
			roots = append(roots, j.Root)
		}
	}
	return roots, nil
}

// rewritePort accepts only the established local placeholder/SDK contract. An
// unrelated provider is left alone, even when it has a familiar display name.
func rewritePort(host, model, origin string, raw []byte) ([]byte, string, bool, error) {
	if host == "workbuddy" {
		doc, err := workBuddyModels(raw)
		if err != nil {
			return nil, "", false, err
		}
		entry, index := workBuddyModel(doc, model)
		if entry == nil || entry["apiKey"] != PlaceholderAPIKey {
			return nil, "", false, nil
		}
		old, ok := entry["url"].(string)
		if ok && strings.TrimRight(old, "/") == origin+"/app/workbuddy/v1" {
			return bytes.Clone(raw), origin, true, nil
		}
		if !ok || entry["supportsToolCall"] != true || entry["useCustomProtocol"] != false {
			return nil, "", false, errors.New("WorkBuddy Router connection fields conflict")
		}
		base := strings.TrimSuffix(strings.TrimRight(old, "/"), "/app/workbuddy/v1")
		if base == strings.TrimRight(old, "/") || !validPortOrigin(base) {
			return nil, "", false, errors.New("invalid WorkBuddy Router endpoint")
		}
		after, err := workBuddyPatchModel(raw, index, map[string]any{"url": origin + "/app/workbuddy/v1"}, nil)
		return after, base, true, err
	}
	doc, err := readJSON(raw)
	if err != nil {
		return nil, "", false, err
	}
	provider, exists := lookup(doc, []string{"provider", loomyProvider})
	if !exists {
		return nil, "", false, nil
	}
	key, _ := lookup(doc, []string{"provider", loomyProvider, "options", "apiKey"})
	if key != PlaceholderAPIKey {
		return nil, "", false, nil
	}
	value, _ := lookup(doc, []string{"provider", loomyProvider, "options", "baseURL"})
	old, ok := value.(string)
	if ok && strings.TrimRight(old, "/") == origin+"/app/loomy/v1" {
		return bytes.Clone(raw), origin, true, nil
	}
	base := strings.TrimSuffix(strings.TrimRight(old, "/"), "/app/loomy/v1")
	wanted := loomyProviderValues(strings.TrimRight(old, "/"), model)
	// Display labels are user settings and do not determine the endpoint.
	if p, ok := provider.(map[string]any); ok {
		if name, exists := p["name"]; exists {
			wanted["name"] = name
		}
		if name, exists := lookup(p, []string{"models", model, "name"}); exists {
			wanted["models"].(map[string]any)[model].(map[string]any)["name"] = name
		}
	}
	if !ok || base == strings.TrimRight(old, "/") || !validPortOrigin(base) ||
		!loomyCompatibleProvider(provider, wanted, model) {
		return nil, "", false, errors.New("Loomy Router connection fields conflict")
	}
	after, err := edit.PatchJSON(raw, []edit.KV{{Path: "provider.api_router.options.baseURL", Value: origin + "/app/loomy/v1"}}, nil)
	return after, base, true, err
}
