package routerhost

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func formatOptions(t *testing.T, host string) Options {
	t.Helper()
	return Options{Host: host, ConfigRoot: t.TempDir(), RouterURL: "http://127.0.0.1:8317", Model: "router-model"}
}
func writeFormatConfig(t *testing.T, o Options, raw []byte) string {
	t.Helper()
	name := "config.toml"
	if o.Host == "claude-code" {
		name = "settings.json"
	}
	p := filepath.Join(o.ConfigRoot, name)
	if err := os.WriteFile(p, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func mustBuildFormat(t *testing.T, o Options) FilePlan {
	t.Helper()
	files, err := BuildFiles(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatal("expected one file")
	}
	return files[0]
}

func TestFormatCreatePreviewApplyAndRestore(t *testing.T) {
	for _, host := range []string{"codex", "claude-code"} {
		t.Run(host, func(t *testing.T) {
			o := formatOptions(t, host)
			f := mustBuildFormat(t, o)
			if !bytes.Contains(f.After, []byte(PlaceholderAPIKey)) {
				t.Fatal("host configuration must contain the public placeholder")
			}
			if f.Existed || len(f.Before) != 0 {
				t.Fatal("missing config must stay missing in preview")
			}
			entries, _ := os.ReadDir(o.ConfigRoot)
			if len(entries) != 0 {
				t.Fatal("preview wrote configuration")
			}
			writeFormatConfig(t, o, f.After)
			again := mustBuildFormat(t, o)
			if !bytes.Equal(again.Before, again.After) {
				t.Fatal("repeated application changed config")
			}
			restored, err := RestoreContent(host, f, f.After)
			if err != nil {
				t.Fatal(err)
			}
			if restored != nil {
				t.Fatal("tool-created unmodified file should be removable")
			}
			restored, err = RestoreContent(host, f, nil)
			if err != nil || len(restored) != 0 {
				t.Fatal("repeated restoration must be unchanged", err)
			}
		})
	}
}

func TestFormatPreservesCommentsUnknownAndCRLF(t *testing.T) {
	fixtures := map[string]string{
		"codex":       "# private comment\r\nmodel = 'previous' # choice\r\nmodel_provider = \"openai\"\r\n[projects.\"/tmp/my-project\"]\r\ntrust_level = 'trusted'\r\n",
		"claude-code": "{\r\n  // keep comment\r\n  \"env\": {\"OTHER_TOKEN\": \"unrelated-secret\", \"ANTHROPIC_MODEL\": \"old-model\"},\r\n  \"permissions\": {\"allow\": [\"Read\"]},\r\n  \"unknown\": {\"value\": 9}\r\n}\r\n",
	}
	for host, fixture := range fixtures {
		t.Run(host, func(t *testing.T) {
			o := formatOptions(t, host)
			path := writeFormatConfig(t, o, []byte(fixture))
			f := mustBuildFormat(t, o)
			disk, _ := os.ReadFile(path)
			if string(disk) != fixture {
				t.Fatal("preview changed existing file")
			}
			needles := []string{"# private comment\r\n", " # choice\r\n", "[projects.\"/tmp/my-project\"]\r\ntrust_level = 'trusted'\r\n"}
			if host == "claude-code" {
				needles = []string{"// keep comment", "\"OTHER_TOKEN\": \"unrelated-secret\"", "\"permissions\": {\"allow\": [\"Read\"]}", "\"unknown\": {\"value\": 9}"}
			}
			for _, needle := range needles {
				if !bytes.Contains(f.After, []byte(needle)) {
					t.Fatalf("lost unrelated bytes %q", needle)
				}
			}
			if bytes.Count(f.After, []byte("\n")) != bytes.Count(f.After, []byte("\r\n")) {
				t.Fatal("lost CRLF")
			}
			restored, err := RestoreContent(host, f, f.After)
			if err != nil || !bytes.Equal(restored, f.Before) {
				t.Fatal("exact restoration failed", err)
			}
		})
	}
}

func TestFormatRestorePreservesLaterUserContent(t *testing.T) {
	for _, host := range []string{"codex", "claude-code"} {
		for _, existing := range []bool{false, true} {
			t.Run(host+"/existing="+strconvBool(existing), func(t *testing.T) {
				o := formatOptions(t, host)
				if existing {
					raw := []byte("model = 'previous' # keep\n[unrelated]\nvalue = 2\n")
					if host == "claude-code" {
						raw = []byte("{\"model\":\"unrelated\",\"env\":{\"OTHER\":\"keep\"}}\n")
					}
					writeFormatConfig(t, o, raw)
				}
				f := mustBuildFormat(t, o)
				var current []byte
				if host == "codex" {
					current = append(append([]byte(nil), f.After...), []byte("\n[user_added]\nvalue = 'preserve me'\n")...)
				} else {
					current = []byte(strings.Replace(string(f.After), "{", "{\"user_added\":\"preserve me\",", 1))
				}
				restored, err := RestoreContent(host, f, current)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(restored, []byte("preserve me")) {
					t.Fatal("deleted user content")
				}
				var doc map[string]any
				if host == "codex" {
					doc, err = readTOML(restored)
				} else {
					doc, err = readJSON(restored)
				}
				if err != nil {
					t.Fatal(err)
				}
				for _, p := range hostFields(host) {
					_, bok := lookupDoc(t, host, f.Before, p)
					got, gok := lookup(doc, p)
					want, _ := lookupDoc(t, host, f.Before, p)
					if !equalValue(want, bok, got, gok) {
						t.Fatal("field not restored", p)
					}
				}
				second, err := RestoreContent(host, f, restored)
				if err != nil || !bytes.Equal(second, restored) {
					t.Fatal("repeated restore changed user content", err)
				}
			})
		}
	}
}
func strconvBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
func lookupDoc(t *testing.T, host string, raw []byte, p []string) (any, bool) {
	t.Helper()
	read := readTOML
	if host == "claude-code" {
		read = readJSON
	}
	doc, err := read(raw)
	if err != nil {
		t.Fatal(err)
	}
	return lookup(doc, p)
}

func TestFormatRestoreRejectsChangedOwnedFieldsWithoutLeakingSecrets(t *testing.T) {
	for _, host := range []string{"codex", "claude-code"} {
		t.Run(host, func(t *testing.T) {
			o := formatOptions(t, host)
			f := mustBuildFormat(t, o)
			current := bytes.ReplaceAll(f.After, []byte(PlaceholderAPIKey), []byte("later-private-token"))
			_, err := RestoreContent(host, f, current)
			if err == nil {
				t.Fatal("later token was overwritten")
			}
			if strings.Contains(err.Error(), PlaceholderAPIKey) || strings.Contains(err.Error(), "later-private-token") {
				t.Fatal("error leaked a credential")
			}
		})
	}
}

func TestFormatPreservesProviderUnknownFieldsAndOriginalSecrets(t *testing.T) {
	o := formatOptions(t, "codex")
	before := []byte("model='old'\n[model_providers.api_router]\n# keep provider note\nname='old name'\nbase_url='https://previous.invalid/v1' # endpoint note\nexperimental_bearer_token='old-secret'\nrequest_max_retries=0\n")
	writeFormatConfig(t, o, before)
	f := mustBuildFormat(t, o)
	if !bytes.Contains(f.After, []byte("# endpoint note")) || !bytes.Contains(f.After, []byte("request_max_retries=0")) {
		t.Fatal("lost provider unknown fields or inline comment")
	}
	current := append(append([]byte(nil), f.After...), []byte("\n[extra]\nuser = true\n")...)
	got, err := RestoreContent(o.Host, f, current)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("\"old-secret\"")) || !bytes.Contains(got, []byte("user = true")) {
		t.Fatal("restore lost original credential or later user content")
	}
}

func TestFormatRefusesDamagedAmbiguousAndUnsupportedConfig(t *testing.T) {
	cases := []struct{ host, raw string }{
		{"codex", "model=\"secret\"\nmodel='another'"}, {"codex", "[model_providers.api_router]\nname=99"}, {"codex", "model = [\"secret\"]"},
		{"codex", "model_providers = {api_router={name='existing'}}"}, {"codex", "[model_providers.api_router]\nenv_key='PRIVATE_KEY'"},
		{"codex", "[model_providers.api_router]\nauth={command='secret'}"}, {"codex", "model = \"unfinished-secret"},
		{"claude-code", "{\"env\":\"secret\"}"}, {"claude-code", "{\"env\":{\"ANTHROPIC_MODEL\":99}}"},
		{"claude-code", "{\"env\":{\"ANTHROPIC_MODEL\":\"a\",\"ANTHROPIC_MODEL\":\"secret\"}}"},
		{"claude-code", "{\"env\":{},\"env\":{\"OTHER\":\"secret\"}}"}, {"claude-code", "{\"private\":\"unfinished-secret"}, {"claude-code", "[]"},
	}
	for _, c := range cases {
		t.Run(c.host+"/"+c.raw, func(t *testing.T) {
			o := formatOptions(t, c.host)
			p := writeFormatConfig(t, o, []byte(c.raw))
			_, err := BuildFiles(o)
			if err == nil {
				t.Fatal("unsafe config accepted")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("parse error exposed source")
			}
			got, _ := os.ReadFile(p)
			if string(got) != c.raw {
				t.Fatal("rejected preview wrote config")
			}
		})
	}
}

func TestFormatEndpointContract(t *testing.T) {
	for _, host := range []string{"codex", "claude-code"} {
		t.Run(host, func(t *testing.T) {
			o := formatOptions(t, host)
			f := mustBuildFormat(t, o)
			field := []string{"model_providers", codexProvider, "base_url"}
			suffix := "/responses"
			if host == "claude-code" {
				field = []string{"env", "ANTHROPIC_BASE_URL"}
				suffix = "/v1/messages"
			}
			value, _ := lookupDoc(t, host, f.After, field)
			want := "http://127.0.0.1:8317/app/" + host + "/v1"
			if host == "codex" {
				want += "/responses"
			} else {
				want += "/messages"
			}
			if value.(string)+suffix != want {
				t.Fatalf("wrong request endpoint: %s%s", value, suffix)
			}
			o.RouterURL = "http://127.0.0.1:8317/app/" + host + "/v1/"
			other := mustBuildFormat(t, o)
			if !bytes.Equal(f.After, other.After) {
				t.Fatal("duplicate v1")
			}
		})
	}
}

func TestFormatRejectsSymlinksRelativeRootsAndBadURLs(t *testing.T) {
	o := formatOptions(t, "codex")
	p := writeFormatConfig(t, o, []byte("model='keep'"))
	linkRoot := t.TempDir()
	if err := os.Symlink(p, filepath.Join(linkRoot, "config.toml")); err != nil {
		t.Skip("symlinks unavailable")
	}
	o.ConfigRoot = linkRoot
	if _, err := BuildFiles(o); err == nil {
		t.Fatal("symlink accepted")
	}
	o.ConfigRoot = "relative"
	if _, err := BuildFiles(o); err == nil {
		t.Fatal("relative root accepted")
	}
	o.ConfigRoot = t.TempDir()
	for _, u := range []string{"ftp://localhost", "http://user:secret@localhost", "http://localhost/v1", "http://localhost?token=secret", "http://localhost/#secret"} {
		o.RouterURL = u
		if _, err := BuildFiles(o); err == nil {
			t.Fatal("invalid URL accepted")
		}
	}
}

func TestFormatNoFinalNewlineAndCommentOnly(t *testing.T) {
	for _, raw := range []string{"model='old'", "# retain comment", "unknown = true", "model='old'\n[model_providers.api_router]\nname='old'"} {
		t.Run(raw, func(t *testing.T) {
			o := formatOptions(t, "codex")
			writeFormatConfig(t, o, []byte(raw))
			mustBuildFormat(t, o)
		})
	}
}

func TestFormatRestorePreservesLaterCommentsOnToolCreatedLines(t *testing.T) {
	o := formatOptions(t, "codex")
	f := mustBuildFormat(t, o)
	current := bytes.Replace(f.After, []byte("model = \"router-model\""), []byte("model = \"router-model\" # user-added-comment"), 1)
	got, err := RestoreContent(o.Host, f, current)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("# user-added-comment")) {
		t.Fatal("user comment deleted")
	}
}

func TestFormatClaudeRestorationKeepsCommentsBesideRemovedFields(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, tail := range []string{
			`"ANTHROPIC_MODEL": "router-model", // user-added-note` + "\n" + `"USER": "keep"`,
			`"USER": "keep",` + "\n" + `"ANTHROPIC_MODEL": "router-model" // user-added-note` + "\n",
			`"ANTHROPIC_MODEL": "router-model" /* user-added-note */, "USER": "keep"`,
			`"ANTHROPIC_MODEL" /* user-added-note */: "router-model", "USER": "keep"`,
			`"ANTHROPIC_MODEL" // user-added-note` + "\n" + `: "router-model", "USER": "keep"`,
		} {
			t.Run(fmt.Sprint(existing, "/", tail), func(t *testing.T) {
				o := formatOptions(t, "claude-code")
				if existing {
					writeFormatConfig(t, o, []byte(`{"unrelated":true}`))
				}
				f := mustBuildFormat(t, o)
				current := []byte("{\n\"env\": {\n" + tail + ",\n\"ANTHROPIC_BASE_URL\": \"http://127.0.0.1:8317/app/claude-code\",\n\"ANTHROPIC_AUTH_TOKEN\": \"api-router-local-placeholder\"\n},\n\"unrelated\":true\n}")
				current = bytes.ReplaceAll(current, []byte("\n"), []byte("\r\n"))
				got, err := RestoreContent(o.Host, f, current)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(got, []byte("user-added-note")) || !bytes.Contains(got, []byte(`"USER": "keep"`)) {
					t.Fatal("removed a later user comment or field")
				}
				if bytes.Contains(got, []byte("\r\r\n")) {
					t.Fatal("duplicated carriage return beside comment")
				}
			})
		}
	}
}
