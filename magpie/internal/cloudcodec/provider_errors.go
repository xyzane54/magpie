package cloudcodec

import (
	"cmp"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

// BlockedHint is what a vendor's edge firewall blocking magpie's address
// means, in plain words: Alibaba Cloud's (ESA, in front of zcode.z.ai)
// answers a 405 HTML page, "Sorry, your request has been blocked due to
// unusual activity", linking errors.aliyun.com. Nothing in the request is
// at fault and magpie changes nothing about it: the address is.
const BlockedHint = "the provider's network firewall blocked requests from this IP; wait a while, or switch to another network or proxy"

// edgeBlocked matches such a block page, whoever's firewall served it.
var EdgeBlocked = regexp.MustCompile(`(?i)request has been blocked|errors\.aliyun\.com`)

// APIError pulls the human message out of an error body when there is one.
// A firewall's block page is put in plain words (BlockedHint); Google's
// "verify your account" refusal also says what to do about it, with the
// link it gave.
func APIError(b []byte, fallback string) string {
	if EdgeBlocked.Match(b) {
		if fallback == "" {
			return BlockedHint
		}
		return fallback + " — " + BlockedHint
	}
	if link, ok := Verification(b); ok {
		return VerifyMessage(ApiError(b, fallback), link)
	}
	if CmdGoNoAPI.Match(b) {
		return ApiError(b, fallback) + " — " + CommandCodeGoHint
	}
	return ApiError(b, fallback)
}

// cmdGoNoAPI is Command Code's Provider API refusing a Go plan's key:
// "Your Go plan doesn't include API access. Upgrade to Provider or higher
// …" (#969). Go's key is only taken where Command Code's CLI asks, which
// the Command Code plugin does; an API-key provider (the "commandcode"
// preset) never can.
var CmdGoNoAPI = regexp.MustCompile(`(?i)\bGo plan doesn.t include API access`)

// CommandCodeGoHint says where a Go plan's key works.
const CommandCodeGoHint = "a Go plan's key works in magpie through the Command Code plugin (Plugins › Command Code, then sign in with the key), which asks Command Code as its CLI does; a Command Code API-key provider can't use it"

// verifyWords is a refusal asking for the account to be verified, put in
// words rather than as VALIDATION_REQUIRED — or already by VerifyMessage.
var VerifyWords = regexp.MustCompile(`(?i)verify your account|account verification required|` + VerifyAdvice)

// verifyLink is the link in a message VerifyMessage put in words.
var VerifyLink = regexp.MustCompile(VerifyAdvice + `: open (https://[^\s"\\]+) in a browser`)

// Verification says whether an error body is Google's refusal of an account
// it wants verified first — Cloud Code Assist's 403 whose details say
// VALIDATION_REQUIRED with a validation_url in their metadata (#152) — and
// the https link to verify it at, when the body has one.
func Verification(b []byte) (link string, ok bool) {
	var v struct {
		Error struct {
			Details []struct {
				Reason   string            `json:"reason"`
				Metadata map[string]string `json:"metadata"`
				Links    []struct {
					URL string `json:"url"`
				} `json:"links"` // google.rpc.Help
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &v) == nil {
		help := ""
		for _, d := range v.Error.Details {
			if d.Reason == "VALIDATION_REQUIRED" {
				ok = true
				link = cmp.Or(link, d.Metadata["validation_url"], d.Metadata["validationUrl"])
			}
			for _, l := range d.Links {
				help = cmp.Or(help, l.URL)
			}
		}
		if ok && link == "" {
			link = help
		}
	}
	if !ok && !VerifyWords.Match(b) {
		return "", false
	}
	if m := VerifyLink.FindSubmatch(b); link == "" && m != nil {
		link = string(m[1]) // said already, by VerifyMessage
	}
	if u, err := url.Parse(link); err != nil || u.Scheme != "https" || u.Host == "" {
		link = ""
	}
	return link, true
}

// verifyAdvice marks a message VerifyMessage has already added to.
const VerifyAdvice = "this Google account needs to be verified"

// VerifyMessage is a verification refusal as the agent and the app show it:
// the vendor's words, then what to do.
func VerifyMessage(said, link string) string {
	said = strings.TrimSpace(said)
	if strings.Contains(said, VerifyAdvice) {
		return said
	}
	if link != "" {
		return said + " — " + VerifyAdvice + ": open " + link + " in a browser signed in to it, verify it, then try again"
	}
	return said + " — " + VerifyAdvice + ": open the Antigravity app (or Gemini CLI) signed in to it and do what it asks, then try again"
}

func ApiError(b []byte, fallback string) string {
	var v struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Msg     string          `json:"msg"`    // Tencent's (WorkBuddy): {code, msg}
		Detail  json.RawMessage `json:"detail"` // FastAPI's (TypeSafe)
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"` // Cloudflare's
	}
	if json.Unmarshal(b, &v) == nil {
		if len(v.Detail) > 0 {
			v.Error = v.Detail
		}
		if len(v.Errors) > 0 && v.Errors[0].Message != "" {
			return v.Errors[0].Message
		}
		var e struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(v.Error, &e) == nil && e.Message != "" {
			return e.Message
		}
		var s string
		if json.Unmarshal(v.Error, &s) == nil && s != "" {
			return s
		}
		if m := cmp.Or(v.Message, v.Msg); m != "" {
			return m
		}
	}
	// a body in no shape known is shown as it is, cut short, so what the
	// vendor said isn't lost; an HTML page says nothing worth showing
	s := strings.Join(strings.Fields(string(b)), " ")
	if s == "" || strings.HasPrefix(s, "<") {
		return fallback
	}
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300]) + "…"
	}
	return fallback + ": " + s
}
