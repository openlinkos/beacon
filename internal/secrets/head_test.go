package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/openlinkos/beacon/internal/i18n"
	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/store"
)

func TestReplayOldItemRowDoesNotRestorePassword(t *testing.T) {
	// red-before: putting an old item overview back restored the rotated-away password.
	bot := &testBot{}
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN, Bot: bot})
	col := newCollection(t, s, "Infra")
	item := putItem(t, s, col.Name, "", "password", fixturePassword)
	var old []byte
	_ = st.Storage().View(func(tx kv.Tx) error {
		old = append([]byte(nil), tx.Get(bucketItems, []byte(item.ID))...)
		return nil
	})
	if _, err := s.SetItem(owner, ItemIn{ID: item.ID, Collection: col.Name,
		Fields: []FieldIn{{Name: "password", Value: fixtureAPIKey}}}); err != nil {
		t.Fatal(err)
	}
	_ = st.Storage().Update(func(tx kv.Tx) error {
		return tx.Put(bucketItems, []byte(item.ID), old)
	})
	if _, _, err := s.Reveal(owner, item.ID, nil, "pw"); err == nil {
		t.Fatal("replaying an old item row must not restore a rotated password")
	}
	if !bot.heard("not found") && !bot.heard("strictest") && len(s.Bad()) == 0 && !bot.heard("stale") {
		t.Error("the stale item was reported")
	}
	if _, err := s.CreateCollection(owner, "Other", "", LevelReview, nil); err != nil {
		t.Fatal(err)
	}
	if values, _, err := s.Reveal(owner, item.ID, nil, "pw"); err == nil && values["password"] == fixturePassword {
		t.Fatal("after an unrelated write the old password is still gone")
	}
	s2 := reopenVault(t, s, st)
	if values, _, err := s2.Reveal(owner, item.ID, nil, "pw"); err == nil && values["password"] == fixturePassword {
		t.Fatal("after reopen the old password is still gone")
	}
}

func TestReplayRevokedManagePermStaysAbsent(t *testing.T) {
	bot := &testBot{}
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN, Bot: bot})
	col := newCollection(t, s, "Infra")
	if err := s.SetPermission(owner, col.Name, "user:bob", PermManage, "bob manages infra"); err != nil {
		t.Fatal(err)
	}
	bob := Actor{Name: "bob", Via: "test", Role: "viewer"}
	if !s.May(bob, col.ID, PermManage) {
		t.Fatal("the grant works")
	}
	var old []byte
	_ = st.Storage().View(func(tx kv.Tx) error {
		old = append([]byte(nil), tx.Get(bucketPerms, permRowKey(col.ID, "user:bob"))...)
		return nil
	})
	if err := s.SetPermission(owner, col.Name, "user:bob", "", "revoked"); err != nil {
		t.Fatal(err)
	}
	if s.May(bob, col.ID, PermManage) {
		t.Fatal("the revoke took")
	}
	_ = st.Storage().Update(func(tx kv.Tx) error {
		return tx.Put(bucketPerms, permRowKey(col.ID, "user:bob"), old)
	})
	if s.May(bob, col.ID, PermManage) {
		t.Fatal("replaying a revoked manage row must not grant")
	}
	if !bot.heard("strictest") && !bot.heard("not in head") && len(s.Bad()) == 0 {
		t.Error("the leftover permission was reported")
	}
	if _, err := s.CreateCollection(owner, "Other", "", LevelReview, nil); err != nil {
		t.Fatal(err)
	}
	if s.May(bob, col.ID, PermManage) {
		t.Fatal("after an unrelated write the revoked row still grants nothing")
	}
	s2 := reopenVault(t, s, st)
	if s2.May(bob, col.ID, PermManage) {
		t.Fatal("after reopen the revoked row still grants nothing")
	}
}

func TestDeletedItemRowStaysMissing(t *testing.T) {
	bot := &testBot{}
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN, Bot: bot})
	col := newCollection(t, s, "Infra")
	item := putItem(t, s, col.Name, "", "password", fixturePassword)
	_ = st.Storage().Update(func(tx kv.Tx) error {
		return tx.Delete(bucketItems, []byte(item.ID))
	})
	if _, _, ok := s.Item(item.ID); ok {
		t.Fatal("a deleted item row the head still names is not found")
	}
	if !bot.heard("not found") && !bot.heard("stale") && !bot.heard("missing") {
		t.Error("the missing item was reported")
	}
	if _, err := s.CreateCollection(owner, "Other", "", LevelReview, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.Item(item.ID); ok {
		t.Fatal("after an unrelated write the deleted item is still not found")
	}
	s2 := reopenVault(t, s, st)
	if _, _, ok := s2.Item(item.ID); ok {
		t.Fatal("after reopen the deleted item is still not found")
	}
}

func TestCorruptPolicyHeadRefusesWrite(t *testing.T) {
	// red-before: an unrelated write rebuilt a missing head by scanning the bucket.
	bot := &testBot{}
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN, Bot: bot})
	col := newCollection(t, s, "Infra")
	if _, err := s.SetTag(owner, "env/prod", LevelNever, "", nil); err != nil {
		t.Fatal(err)
	}
	item, err := s.SetItem(owner, ItemIn{Collection: col.Name, Title: "Tagged", Tags: []string{"env/prod"},
		Fields: []FieldIn{{Name: "password", Value: fixturePassword}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Storage().Update(func(tx kv.Tx) error {
		if err := tx.Delete(bucketMeta, headKey); err != nil {
			return err
		}
		return tx.Delete(bucketTags, []byte("env/prod"))
	})
	if _, err := s.CreateCollection(owner, "Other", "", LevelReview, nil); !errors.Is(err, ErrHead) {
		t.Fatalf("an unrelated write must refuse while the head is missing, got %v", err)
	}
	if got := s.EffectiveLevel(item); got != LevelNever {
		t.Fatalf("failsafe holds, got %s", got)
	}
	s2 := reopenVault(t, s, st)
	if _, err := s2.CreateCollection(owner, "Later", "", LevelReview, nil); !errors.Is(err, ErrHead) {
		t.Fatalf("open again still refuses writes, got %v", err)
	}
	if got := s2.EffectiveLevel(item); got != LevelNever {
		t.Fatalf("open again failsafe holds, got %s", got)
	}
}

func TestCorruptItemsHeadRefusesWrite(t *testing.T) {
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN})
	col := newCollection(t, s, "Infra")
	item := putItem(t, s, col.Name, "", "password", fixturePassword)
	_ = st.Storage().Update(func(tx kv.Tx) error {
		if err := tx.Delete(bucketItems, headKey); err != nil {
			return err
		}
		return tx.Delete(bucketItems, []byte(item.ID))
	})
	if _, err := s.SetItem(owner, ItemIn{Collection: col.Name, Title: "Other",
		Fields: []FieldIn{{Name: "password", Value: fixtureAPIKey}}}); !errors.Is(err, ErrHead) {
		t.Fatalf("an unrelated item write must refuse while the items head is missing, got %v", err)
	}
	if _, _, ok := s.Item(item.ID); ok {
		t.Fatal("the deleted item stays not found")
	}
	s2 := reopenVault(t, s, st)
	if _, err := s2.SetItem(owner, ItemIn{Collection: col.Name, Title: "Later",
		Fields: []FieldIn{{Name: "password", Value: fixtureAPIKey}}}); !errors.Is(err, ErrHead) {
		t.Fatalf("open again still refuses, got %v", err)
	}
}

func TestStalePolicyHeadAndRowsAreFailsafe(t *testing.T) {
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN})
	if _, err := s.SetTag(owner, "env/prod", LevelAuto, "", nil); err != nil {
		t.Fatal(err)
	}
	var oldHead, oldRow []byte
	_ = st.Storage().View(func(tx kv.Tx) error {
		oldHead = append([]byte(nil), tx.Get(bucketMeta, headKey)...)
		oldRow = append([]byte(nil), tx.Get(bucketTags, []byte("env/prod"))...)
		return nil
	})
	if _, err := s.SetTag(owner, "env/prod", LevelNever, "", nil); err != nil {
		t.Fatal(err)
	}
	col := newCollection(t, s, "Infra")
	item, err := s.SetItem(owner, ItemIn{Collection: col.Name, Title: "Tagged", Tags: []string{"env/prod"},
		Fields: []FieldIn{{Name: "password", Value: fixturePassword}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Storage().Update(func(tx kv.Tx) error {
		if err := tx.Put(bucketMeta, headKey, oldHead); err != nil {
			return err
		}
		return tx.Put(bucketTags, []byte("env/prod"), oldRow)
	})
	if got := s.EffectiveLevel(item); got != LevelNever {
		t.Fatalf("stale pair must stay never, got %s", got)
	}
	s2 := reopenVault(t, s, st)
	if got := s2.EffectiveLevel(item); got != LevelNever {
		t.Fatalf("open again: stale pair must stay never, got %s", got)
	}
}

func TestStaleItemsHeadAndRowsAreFailsafe(t *testing.T) {
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN})
	col := newCollection(t, s, "Infra")
	item := putItem(t, s, col.Name, "", "password", fixturePassword)
	var oldHead, oldRow []byte
	_ = st.Storage().View(func(tx kv.Tx) error {
		oldHead = append([]byte(nil), tx.Get(bucketItems, headKey)...)
		oldRow = append([]byte(nil), tx.Get(bucketItems, []byte(item.ID))...)
		return nil
	})
	if _, err := s.SetItem(owner, ItemIn{ID: item.ID, Collection: col.Name,
		Fields: []FieldIn{{Name: "password", Value: fixtureAPIKey}}}); err != nil {
		t.Fatal(err)
	}
	_ = st.Storage().Update(func(tx kv.Tx) error {
		if err := tx.Put(bucketItems, headKey, oldHead); err != nil {
			return err
		}
		return tx.Put(bucketItems, []byte(item.ID), oldRow)
	})
	if values, _, err := s.Reveal(owner, item.ID, nil, "pw"); err == nil && values["password"] == fixturePassword {
		t.Fatal("stale item pair must not restore the old password")
	}
	s2 := reopenVault(t, s, st)
	if values, _, err := s2.Reveal(owner, item.ID, nil, "pw"); err == nil && values["password"] == fixturePassword {
		t.Fatal("open again: stale item pair must not restore the old password")
	}
}

func TestTamperThenUnrelatedWriteStaysInvalid(t *testing.T) {
	bot := &testBot{}
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN, Bot: bot})
	if _, err := s.SetTag(owner, "env/prod", LevelNever, "", nil); err != nil {
		t.Fatal(err)
	}
	col := newCollection(t, s, "Infra")
	item, err := s.SetItem(owner, ItemIn{Collection: col.Name, Title: "Tagged", Tags: []string{"env/prod"},
		Fields: []FieldIn{{Name: "password", Value: fixturePassword}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Storage().Update(func(tx kv.Tx) error {
		v := tx.Get(bucketTags, []byte("env/prod"))
		v[len(v)-1] ^= 0xff
		return tx.Put(bucketTags, []byte("env/prod"), v)
	})
	if got := s.EffectiveLevel(item); got != LevelNever {
		t.Fatalf("tampered tag stays never, got %s", got)
	}
	if _, err := s.CreateCollection(owner, "Other", "", LevelReview, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.EffectiveLevel(item); got != LevelNever {
		t.Fatalf("after an unrelated write the tamper still stays never, got %s", got)
	}
	if !bot.heard("strictest") && len(s.Bad()) == 0 {
		t.Error("the tamper was reported")
	}
	s2 := reopenVault(t, s, st)
	if got := s2.EffectiveLevel(item); got != LevelNever {
		t.Fatalf("after reopen the tamper still stays never, got %s", got)
	}
}

func TestRebuildHeadRestoresAccept(t *testing.T) {
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN})
	col := newCollection(t, s, "Infra")
	item := putItem(t, s, col.Name, "", "password", fixturePassword)
	if _, err := s.SetTag(owner, "env/prod", LevelNever, "", nil); err != nil {
		t.Fatal(err)
	}
	_ = st.Storage().Update(func(tx kv.Tx) error {
		return tx.Delete(bucketMeta, headKey)
	})
	s2 := reopenVault(t, s, st)
	sStay, err := Open(context.Background(), SealConfig{Mode: s2.cfg.Mode, Dir: s2.cfg.Dir}, Deps{Store: st, Lang: i18n.EN, Log: testLog(t),
		CheckPassword: func(string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.CreateCollection(owner, "Other", "", LevelReview, nil); !errors.Is(err, ErrHead) {
		t.Fatalf("without rebuild writes refuse, got %v", err)
	}
	if err := s2.RebuildHead(owner); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.CreateCollection(owner, "Other", "", LevelReview, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s2.Item(item.ID); !ok {
		t.Fatal("after rebuild the item is there")
	}
	if _, err := sStay.CreateCollection(owner, "Later", "", LevelReview, nil); !errors.Is(err, ErrHead) {
		t.Fatalf("a service that did not rebuild stays failsafe, got %v", err)
	}
	entries, _, err := st.AuditQuery(store.AuditFilter{Action: "secrets_head_rebuild"})
	if err != nil || len(entries) == 0 {
		t.Fatalf("rebuild not audited: %+v %v", entries, err)
	}
}

func TestWriteDoesNotScanSneakedRow(t *testing.T) {
	s, st := newVault(t, SealFile, Deps{Lang: i18n.EN})
	if _, err := s.SetTag(owner, "env/prod", LevelNever, "", nil); err != nil {
		t.Fatal(err)
	}
	sk, err := s.sk()
	if err != nil {
		t.Fatal(err)
	}
	sneak := Tag{Name: "env/dev", Level: LevelAuto}
	r := signedTag{Tag: sneak}
	r.Sig = signRow(sk, sneak.signedParts()...)
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Storage().Update(func(tx kv.Tx) error {
		return tx.Put(bucketTags, []byte(sneak.Name), b)
	})
	if _, err := s.SetTag(owner, "env/stage", LevelReview, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.tagByName("env/dev"); ok {
		t.Fatal("a sneaked tag must not become policy")
	}
	col := newCollection(t, s, "Infra")
	item, err := s.SetItem(owner, ItemIn{Collection: col.Name, Title: "Tagged", Tags: []string{"env/dev"},
		Fields: []FieldIn{{Name: "password", Value: fixturePassword}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.EffectiveLevel(item); got != col.Level {
		t.Fatalf("a sneaked tag is a label, got %s", got)
	}
	oldItem := putItem(t, s, col.Name, "", "password", fixturePassword)
	var sneakItem []byte
	_ = st.Storage().View(func(tx kv.Tx) error {
		sneakItem = append([]byte(nil), tx.Get(bucketItems, []byte(oldItem.ID))...)
		return nil
	})
	sneakID := "deadbeefdeadbeef"
	_ = st.Storage().Update(func(tx kv.Tx) error {
		return tx.Put(bucketItems, []byte(sneakID), sneakItem)
	})
	if _, err := s.SetItem(owner, ItemIn{Collection: col.Name, Title: "Another",
		Fields: []FieldIn{{Name: "password", Value: fixtureAPIKey}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.Item(sneakID); ok {
		t.Fatal("a sneaked item row must not be named by the items head")
	}
}

func TestRebuildHeadNeedsOwner(t *testing.T) {
	s, _ := newVault(t, SealFile, Deps{Lang: i18n.EN})
	if err := s.RebuildHead(viewer); !errors.Is(err, ErrForbidden) {
		t.Fatalf("rebuild is the owner's recovery, got %v", err)
	}
}

func TestEpochsFileLivesInSecretsDir(t *testing.T) {
	s, _ := newVault(t, SealFile, Deps{Lang: i18n.EN})
	newCollection(t, s, "Infra")
	if _, err := os.Stat(filepath.Join(s.cfg.Dir, "epochs")); err != nil {
		t.Fatalf("the epochs file lives in the secrets directory: %v", err)
	}
}
