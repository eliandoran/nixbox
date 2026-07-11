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

// TestSecretMountsInvalidWorkload: foreign keys are ON, so mounting
// into a nonexistent workload fails the whole create/update.
func TestSecretMountsInvalidWorkload(t *testing.T) {
	s := open(t)
	if _, err := s.CreateSecret("bad", "root", "root", "0400", []int64{9999}); err == nil {
		t.Error("expected FK violation on create with bogus workload")
	}
	sec, err := s.CreateSecret("ok", "root", "root", "0400", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSecret(sec.ID, "root", "root", "0400", []int64{9999}); err == nil {
		t.Error("expected FK violation on update with bogus workload")
	}
}

// TestSecretsClosedDB drives every method over a closed handle so the
// query/exec error returns are exercised.
func TestSecretsClosedDB(t *testing.T) {
	s := open(t)
	sec, err := s.CreateSecret("x", "root", "root", "0400", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	if _, err := s.CreateSecret("y", "root", "root", "0400", nil); err == nil {
		t.Error("CreateSecret on closed db")
	}
	if err := s.UpdateSecret(sec.ID, "root", "root", "0400", nil); err == nil {
		t.Error("UpdateSecret on closed db")
	}
	if _, err := s.SecretByID(sec.ID); err == nil {
		t.Error("SecretByID on closed db")
	}
	if _, err := s.SecretByName("x"); err == nil {
		t.Error("SecretByName on closed db")
	}
	if _, err := s.Secrets(); err == nil {
		t.Error("Secrets on closed db")
	}
	if err := s.AddSecretMount(sec.ID, 1); err == nil {
		t.Error("AddSecretMount on closed db")
	}
	if err := s.RemoveSecretMount(sec.ID, 1); err == nil {
		t.Error("RemoveSecretMount on closed db")
	}
	if err := s.MarkSecretApplied(sec.ID); err == nil {
		t.Error("MarkSecretApplied on closed db")
	}
	if err := s.TouchSecret(sec.ID); err == nil {
		t.Error("TouchSecret on closed db")
	}
	if err := s.DeleteSecret(sec.ID); err == nil {
		t.Error("DeleteSecret on closed db")
	}
}

// TestSecretsMountQueryError faults only the mounts lookup (table
// dropped) so loadMounts' own query error path runs — both through a
// single lookup and through the Secrets() list.
func TestSecretsMountQueryError(t *testing.T) {
	s := open(t)
	if _, err := s.CreateSecret("x", "root", "root", "0400", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE secret_mounts`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SecretByName("x"); err == nil {
		t.Error("expected mounts query error on lookup")
	}
	if _, err := s.Secrets(); err == nil {
		t.Error("expected mounts query error on list")
	}
}

// TestSecretsFaultedRows exercises the row-level error paths that a
// healthy schema can't produce: failing statements inside the update
// transaction, an unscannable secret row, and an unscannable mount row.
func TestSecretsFaultedRows(t *testing.T) {
	// UpdateSecret's UPDATE fails when the secrets table is gone.
	s := open(t)
	sec, err := s.CreateSecret("x", "root", "root", "0400", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE secrets`); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSecret(sec.ID, "root", "root", "0400", nil); err == nil {
		t.Error("expected update error without secrets table")
	}

	// replaceMounts' DELETE fails when only secret_mounts is gone.
	s = open(t)
	if sec, err = s.CreateSecret("x", "root", "root", "0400", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE secret_mounts`); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSecret(sec.ID, "root", "root", "0400", nil); err == nil {
		t.Error("expected mount-clear error without secret_mounts table")
	}

	// A row whose timestamp column holds junk fails the list scan.
	s = open(t)
	if _, err := s.db.Exec(
		`INSERT INTO secrets (name, owner, group_name, mode, created_at, updated_at)
		 VALUES ('bad', 'root', 'root', '0400', 'not-a-time', 'not-a-time')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Secrets(); err == nil {
		t.Error("expected scan error on corrupt timestamp")
	}

	// A junk workload_id (FKs bypassed) fails the mounts scan.
	s = open(t)
	if sec, err = s.CreateSecret("x", "root", "root", "0400", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO secret_mounts (secret_id, workload_id) VALUES (?, 'junk')`, sec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SecretByID(sec.ID); err == nil {
		t.Error("expected scan error on junk workload_id")
	}
}

// TestSecretMountAddRemove covers the workload-page flow: single-mount
// add/remove, idempotence, and the badge only flipping on real change.
func TestSecretMountAddRemove(t *testing.T) {
	s := open(t)
	web, err := s.CreateWorkload("web", "", "nixos-container", "{ }\n", "")
	if err != nil {
		t.Fatal(err)
	}
	sec, err := s.CreateSecret("token", "root", "root", "0400", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSecretApplied(sec.ID); err != nil {
		t.Fatal(err)
	}

	// Attach: mount appears, badge reopens.
	if err := s.AddSecretMount(sec.ID, web.ID); err != nil {
		t.Fatal(err)
	}
	sec, _ = s.SecretByID(sec.ID)
	if !sec.MountedInto(web.ID) || sec.Status() != "pending" {
		t.Errorf("after attach: mounts=%v status=%s", sec.WorkloadIDs, sec.Status())
	}

	// Re-attach is a no-op: applied state must survive.
	if err := s.MarkSecretApplied(sec.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSecretMount(sec.ID, web.ID); err != nil {
		t.Fatal(err)
	}
	sec, _ = s.SecretByID(sec.ID)
	if sec.Status() != "applied" {
		t.Errorf("duplicate attach flipped badge: %s", sec.Status())
	}

	// Detach: mount gone, badge reopens.
	if err := s.RemoveSecretMount(sec.ID, web.ID); err != nil {
		t.Fatal(err)
	}
	sec, _ = s.SecretByID(sec.ID)
	if sec.MountedInto(web.ID) || sec.Status() != "pending" {
		t.Errorf("after detach: mounts=%v status=%s", sec.WorkloadIDs, sec.Status())
	}
}
