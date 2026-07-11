package store

import "testing"

func TestSecretLifecycle(t *testing.T) {
	s := open(t)

	web, err := s.CreateWorkload("web", "", "nixos-container", "{ }\n", "")
	if err != nil {
		t.Fatal(err)
	}
	app, err := s.CreateWorkload("app", "", "oci-container", "{ }\n", "")
	if err != nil {
		t.Fatal(err)
	}

	sec, err := s.CreateSecret("db-pass", "root", "root", "0400", []int64{web.ID})
	if err != nil {
		t.Fatal(err)
	}
	if sec.Status() != "pending" {
		t.Errorf("new secret status = %q, want pending", sec.Status())
	}
	if !sec.MountedInto(web.ID) || sec.MountedInto(app.ID) {
		t.Errorf("unexpected mounts: %v", sec.WorkloadIDs)
	}

	// Duplicate names are rejected (UNIQUE): the name is the agenix key
	// and the on-disk filename.
	if _, err := s.CreateSecret("db-pass", "root", "root", "0400", nil); err == nil {
		t.Error("expected duplicate name to fail")
	}

	if err := s.MarkSecretApplied(sec.ID); err != nil {
		t.Fatal(err)
	}
	sec, err = s.SecretByName("db-pass")
	if err != nil {
		t.Fatal(err)
	}
	if sec.Status() != "applied" {
		t.Errorf("status after apply = %q, want applied", sec.Status())
	}

	// An edit reopens the badge and replaces the mounts wholesale.
	if err := s.UpdateSecret(sec.ID, "nginx", "nginx", "0440", []int64{web.ID, app.ID}); err != nil {
		t.Fatal(err)
	}
	sec, err = s.SecretByID(sec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sec.Status() != "pending" || sec.Owner != "nginx" || sec.Mode != "0440" {
		t.Errorf("after update: %+v", sec)
	}
	if len(sec.WorkloadIDs) != 2 {
		t.Errorf("mounts after update: %v", sec.WorkloadIDs)
	}

	// Destroying a workload cascades its mounts but keeps the secret.
	if err := s.DeleteWorkload(app.ID); err != nil {
		t.Fatal(err)
	}
	sec, err = s.SecretByID(sec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sec.WorkloadIDs) != 1 || sec.WorkloadIDs[0] != web.ID {
		t.Errorf("mounts after workload delete: %v", sec.WorkloadIDs)
	}

	all, err := s.Secrets()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Name != "db-pass" || len(all[0].WorkloadIDs) != 1 {
		t.Errorf("Secrets() = %+v", all)
	}

	if err := s.DeleteSecret(sec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SecretByID(sec.ID); err != ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}
