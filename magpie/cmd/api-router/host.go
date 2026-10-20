package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/yetone/magpie/internal/dirs"
	"github.com/yetone/magpie/internal/routerhost"
)

// Management credentials are used only for the management API, never persisted
// in the host configuration or recovery journal.
var hostChecker = func(managementToken string) routerhost.Checker {
	return routerhost.ProductionChecker(managementToken)
}

var workBuddyHostChecker = func(managementToken string) routerhost.Checker {
	return routerhost.ProductionWorkBuddyChecker(managementToken)
}

const hostHelp = `Usage: api-router host <preview|apply|restore-preview|restore> [options]

All commands require:
  --host codex|claude-code|loomy|workbuddy  Host to configure
  --config-root ABSOLUTE_DIR    Explicit host configuration directory
  --state-dir ABSOLUTE_DIR      Private backup directory (default: API-router state/host-config)

preview and apply also require:
  --router-url ORIGIN           Router origin, for example http://127.0.0.1:13145
  --model MODEL                 Application model name

apply additionally requires:
  --management-token-env NAME    Management token variable, used only for GET /api/apps

apply and restore also require:
  --plan-digest DIGEST           Digest printed by the corresponding preview

preview writes nothing. apply checks the service, application and model entry before writing.
restore-preview and restore work with Router stopped and bypass Router after recovery.
Restore preserves unrelated settings and refuses conflicting changes to managed fields.
restore emits JSON Lines: a notice before writing, then the result after success.
Use the same --state-dir for preview, apply and recovery. Keep backups private.
Host configuration uses a public non-secret placeholder; no application token is needed.
Management tokens are never sent to model paths.
The management token is not read by preview or restore; it is not stored in host files or backups.
WorkBuddy 5.3.8 uses models.json and settings.json in the explicitly selected directory.
For WorkBuddy, --model is the wire model ID without the custom-local: selection prefix.
An unavailable app API or model protocol blocks apply without host changes.
No token is accepted as a CLI value.
Use --help at any command level to print this help without changing files.
`

var hostEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func runHost(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		_, err := io.WriteString(out, hostHelp)
		return err
	}
	for _, arg := range args {
		if arg == "--help" || arg == "-h" || arg == "help" {
			_, err := io.WriteString(out, hostHelp)
			return err
		}
	}
	action := args[0]
	recovering := action == "restore-preview" || action == "restore"
	if action != "preview" && action != "apply" && !recovering {
		return fmt.Errorf("unknown host command; use api-router host --help")
	}
	flags := flag.NewFlagSet("host "+action, flag.ContinueOnError)
	// Parse errors may contain input. Report only the supported usage below.
	flags.SetOutput(io.Discard)
	var options routerhost.Options
	var stateDir, tokenEnv, managementEnv, digest string
	flags.StringVar(&options.Host, "host", "", "host")
	flags.StringVar(&options.ConfigRoot, "config-root", "", "host configuration directory")
	flags.StringVar(&stateDir, "state-dir", "", "private backup directory")
	if !recovering {
		flags.StringVar(&options.RouterURL, "router-url", "", "Router origin")
		flags.StringVar(&options.Model, "model", "", "model")
		flags.StringVar(&tokenEnv, "token-env", "", "obsolete: application tokens are no longer used")
	}
	if action == "apply" {
		flags.StringVar(&managementEnv, "management-token-env", "", "management token environment variable")
	}
	if action == "apply" || action == "restore" {
		flags.StringVar(&digest, "plan-digest", "", "preview digest")
	}
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return fmt.Errorf("invalid host options; use api-router host %s --help", action)
	}
	if options.Host != "codex" && options.Host != "claude-code" && options.Host != "loomy" && options.Host != "workbuddy" {
		return fmt.Errorf("--host must be codex, claude-code, loomy or workbuddy")
	}
	if !filepath.IsAbs(options.ConfigRoot) {
		return fmt.Errorf("--config-root must be an explicit absolute host configuration directory")
	}
	if stateDir == "" {
		layout, err := dirs.Resolve()
		if err != nil {
			return err
		}
		stateDir = filepath.Join(layout.State, "host-config")
	} else if !filepath.IsAbs(stateDir) {
		return fmt.Errorf("--state-dir must be an absolute private directory")
	}
	if !recovering {
		if strings.TrimSpace(options.RouterURL) == "" || strings.TrimSpace(options.Model) == "" || strings.HasPrefix(options.Model, "-") {
			return fmt.Errorf("--router-url and --model are required; use api-router host %s --help", action)
		}
		legacyTokenOption := false
		flags.Visit(func(f *flag.Flag) {
			if f.Name == "token-env" {
				legacyTokenOption = true
			}
		})
		if legacyTokenOption {
			return fmt.Errorf("--token-env is obsolete: Router applications no longer use entry keys; remove this option")
		}
	}
	if (action == "apply" || action == "restore") && digest == "" {
		return fmt.Errorf("--plan-digest is required; run the corresponding preview first")
	}
	engine := routerhost.Engine{StateDir: stateDir}
	switch options.Host {
	case "loomy":
		engine.Adapter = routerhost.LoomyAdapter{}
	case "workbuddy":
		engine.Adapter = routerhost.WorkBuddyAdapter{}
	}
	var plan *routerhost.Plan
	var err error
	if recovering {
		plan, err = engine.PreviewRestore(options.Host, options.ConfigRoot)
	} else {
		plan, err = engine.Preview(options)
	}
	if err != nil {
		return err
	}
	summary, err := hostPlanSummary(plan, options)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	if action != "restore" {
		encoder.SetIndent("", "  ")
	}
	if action == "apply" || action == "restore" {
		if subtle.ConstantTimeCompare([]byte(plan.Digest), []byte(digest)) != 1 {
			return fmt.Errorf("host configuration plan changed; run the corresponding preview again and use its --plan-digest")
		}
		if action == "apply" {
			managementToken := ""
			if managementEnv != "" {
				if !hostEnvName.MatchString(managementEnv) {
					return fmt.Errorf("--management-token-env must name an environment variable")
				}
				managementToken = os.Getenv(managementEnv)
				if strings.TrimSpace(managementToken) == "" || strings.ContainsAny(managementToken, "\r\n") {
					return fmt.Errorf("management token environment variable is unset or invalid; use a current management token")
				}
			}
			switch options.Host {
			case "loomy":
				engine.Checker = routerhost.LoomyChecker(managementToken)
			case "workbuddy":
				engine.Checker = workBuddyHostChecker(managementToken)
			default:
				engine.Checker = hostChecker(managementToken)
			}
			err = engine.Apply(ctx, plan)
		} else {
			if err := encoder.Encode(struct {
				Action string `json:"action"`
				Stage  string `json:"stage"`
				Notice string `json:"notice"`
			}{action, "before-write", "Restoring bypasses API-router and returns model access to the previous host settings."}); err != nil {
				return fmt.Errorf("cannot print recovery notice; no host configuration was changed: %w", err)
			}
			err = engine.Restore(plan)
		}
		if err != nil {
			return err
		}
	}
	notice := "Preview only; no host configuration was changed."
	if action == "apply" {
		notice = "Host configuration applied after connection checks."
	} else if recovering {
		notice = "Restoring bypasses API-router and returns model access to the previous host settings."
		if action == "restore-preview" {
			notice += " Preview only; no host configuration was changed."
		}
	}
	return encoder.Encode(struct {
		Action string `json:"action"`
		Host   string `json:"host"`
		Digest string `json:"digest"`
		Notice string `json:"notice"`
		Plan   any    `json:"plan"`
	}{action, plan.Host, plan.Digest, notice, summary})
}

// WorkBuddy's installed URL builder and selection prefix belong to its CLI
// presentation; the generic transaction's safe Summary stays unchanged.
func hostPlanSummary(plan *routerhost.Plan, options routerhost.Options) (any, error) {
	if options.Host != "workbuddy" {
		return plan.Summary(), nil
	}
	raw, err := json.Marshal(plan.Summary())
	if err != nil {
		return nil, fmt.Errorf("cannot prepare host plan output")
	}
	var summary map[string]any
	if json.Unmarshal(raw, &summary) != nil {
		return nil, fmt.Errorf("cannot prepare host plan output")
	}
	summary["HostVersion"] = "WorkBuddy 5.3.8"
	summary["CredentialSource"] = "public non-secret placeholder: " + routerhost.PlaceholderAPIKey
	summary["ModelSelection"] = ""
	if options.RouterURL != "" {
		summary["RouterURL"] = strings.TrimRight(options.RouterURL, "/") + "/app/workbuddy/v1"
		summary["RequestURL"] = strings.TrimRight(options.RouterURL, "/") + "/app/workbuddy/v1/chat/completions"
		summary["ModelSelection"] = "custom-local:" + options.Model
	}
	return summary, nil
}
