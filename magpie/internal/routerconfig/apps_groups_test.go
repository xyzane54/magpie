package routerconfig

import "testing"

func TestDeleteCloudProviderReferencedByAppModelsOrGroup(t *testing.T) {
	s := providerSetup(t)

	p1Draft := providerDraft()
	p1Draft.Name = "Primary Provider"
	p1, err := s.SaveCloudProvider(p1Draft)
	if err != nil {
		t.Fatal(err)
	}

	p2Draft := providerDraft()
	p2Draft.Name = "Secondary Provider"
	p2, err := s.SaveCloudProvider(p2Draft)
	if err != nil {
		t.Fatal(err)
	}

	p3Draft := providerDraft()
	p3Draft.Name = "Group Provider"
	p3, err := s.SaveCloudProvider(p3Draft)
	if err != nil {
		t.Fatal(err)
	}

	// Bind workbuddy where Models[0] is local/* (so Cloud.ProviderID == ""), and Models[1] references p2 ("<p2.ID>/m2").
	wb, err := s.SaveApp(AppEdit{
		ID:             "workbuddy",
		Name:           "WorkBuddy",
		CredentialMode: StoredCredential,
		Enabled:        boolPtr(true),
		Models:         slicePtr("local/spark-x2.5-1.7b", p2.ID+"/m2"),
	})
	if err != nil {
		t.Fatalf("SaveApp(workbuddy): %v", err)
	}
	if wb.Cloud.ProviderID != "" {
		t.Fatalf("expected empty Cloud.ProviderID, got %q", wb.Cloud.ProviderID)
	}
	if target, err := s.ResolveApp("workbuddy", "", ""); err != nil || target.Profile.ID != "workbuddy" {
		t.Fatalf("expected ResolveApp to succeed when Models is configured and Cloud.ProviderID is empty, got target=%+v err=%v", target, err)
	}

	// Deleting p2 must fail because workbuddy.Models[1] references "<p2.ID>/m2"
	if err := s.DeleteCloudProvider(p2.ID, p2.Revision); err == nil {
		t.Fatal("expected DeleteCloudProvider(p2) to fail when referenced in app.Models[1]")
	}

	// Update workbuddy to remove reference to p2; now DeleteCloudProvider(p2) should succeed
	if _, err := s.SaveApp(AppEdit{
		ID:             "workbuddy",
		Revision:       wb.Revision,
		Name:           "WorkBuddy",
		CredentialMode: StoredCredential,
		Enabled:        boolPtr(true),
		Models:         slicePtr("local/spark-x2.5-1.7b"),
	}); err != nil {
		t.Fatalf("SaveApp(workbuddy) unbind p2: %v", err)
	}
	if err := s.DeleteCloudProvider(p2.ID, p2.Revision); err != nil {
		t.Fatalf("expected DeleteCloudProvider(p2) to succeed after unbinding, got: %v", err)
	}

	// Create an active routing group referencing p3 ("<p3.ID>/glm-5")
	if _, err := s.SaveGroup(Group{
		ID:      "coding-group",
		Name:    "Coding Group",
		Members: []string{p3.ID + "/glm-5", p1.ID + "/gpt-4o"},
	}); err != nil {
		t.Fatalf("SaveGroup: %v", err)
	}

	// Deleting p3 must fail because active group "coding-group" references "<p3.ID>/glm-5"
	if err := s.DeleteCloudProvider(p3.ID, p3.Revision); err == nil {
		t.Fatal("expected DeleteCloudProvider(p3) to fail when referenced in active group.Members")
	}

	// If the group is disabled (or deleted), deleting p3 should succeed
	if _, err := s.DeleteGroup("coding-group"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if err := s.DeleteCloudProvider(p3.ID, p3.Revision); err != nil {
		t.Fatalf("expected DeleteCloudProvider(p3) to succeed after group deleted, got: %v", err)
	}
}
