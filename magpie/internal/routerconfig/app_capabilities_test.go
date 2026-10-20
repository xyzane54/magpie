package routerconfig

import (
	"reflect"
	"testing"
)

func TestApplicationToolCapabilitiesStayWithExactTarget(t *testing.T) {
	s := providerSetup(t)
	draft := providerDraft()
	draft.Protocol = "responses"
	p, err := s.SaveCloudProvider(draft)
	if err != nil {
		t.Fatal(err)
	}
	in := appDraft("codex", p.ID)
	in.Cloud.Capabilities = []string{"namespace", "custom", "additional_tools", "web_search"}
	saved, err := s.SaveApp(in)
	if err != nil {
		t.Fatal(err)
	}
	target, err := New(s.layout).ResolveApp("codex", "", "")
	if err != nil || !reflect.DeepEqual(target.Profile.Cloud.Capabilities, in.Cloud.Capabilities) {
		t.Fatal("capabilities did not survive persisted target resolution", err)
	}
	// An older editor may omit the new field while editing the same binding.
	in.Revision = saved.Revision
	in.Cloud.Capabilities = nil
	saved, err = s.SaveApp(in)
	if err != nil || len(saved.Cloud.Capabilities) != 4 {
		t.Fatal("editor erased declarations", err)
	}
	in.Revision = saved.Revision
	in.Cloud.Model = "different-model"
	saved, err = s.SaveApp(in)
	if err != nil || len(saved.Cloud.Capabilities) != 0 {
		t.Fatal("new model inherited capabilities", err)
	}
	in.Revision = saved.Revision
	in.Cloud.Capabilities = []string{"namespace"}
	saved, err = s.SaveApp(in)
	if err != nil {
		t.Fatal(err)
	}
	in.Revision = saved.Revision
	in.Cloud.Capabilities = []string{}
	saved, err = s.SaveApp(in)
	if err != nil || len(saved.Cloud.Capabilities) != 0 {
		t.Fatal("explicit clear failed", err)
	}
	target, err = New(s.layout).ResolveApp("codex", "", "")
	if err != nil || len(target.Profile.Cloud.Capabilities) != 0 {
		t.Fatal("persisted clear failed", err)
	}
	for _, capabilities := range [][]string{{"all"}, {"namespace", "namespace"}} {
		in.Revision = saved.Revision
		in.Cloud.Capabilities = capabilities
		if _, err := s.SaveApp(in); err == nil {
			t.Fatal("invalid capability declaration accepted")
		}
	}
}
