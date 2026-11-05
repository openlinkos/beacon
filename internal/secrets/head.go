package secrets

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/signedhead"
	"github.com/openlinkos/beacon/internal/store"
)

// The signed heads of the vault share one epochs file in the secrets directory:
// "policy" (collections, tag-policy rows, people permissions) in secret_meta,
// "items" (item overviews) in secret_items,
// "eligibility" (entitlements and agent grouping tags) in secret_entitlements,
// "grants" (active grants only) in secret_grants.
// A write updates only the keys that transaction put or deleted; Beacon never rebuilds a head by
// scanning the bucket except RebuildHead. Holding the database file alone cannot widen a
// permission, drop a deny, or resurrect a revoked grant.

var headKey = []byte("\x00head")

const (
	kindCollection  = "collection"
	kindTag         = "tag"
	kindPerm        = "perm"
	kindItem        = "item"
	kindEntitlement = "entitlement"
	kindGrant       = "grant"
	kindAgentTags   = "agent-tags"

	headPolicy      = "policy"
	headItems       = "items"
	headEligibility = "eligibility"
	headGrants      = "grants"

	entitlementDeny  = "deny"
	entitlementAllow = "allow"
)

func policyRowKey(kind, key string) string { return kind + "\x00" + key }

func isHeadKey(k []byte) bool { return string(k) == string(headKey) }

// entitlementHeadKey names a deny or allow in the eligibility head. The side lives in the key
// because the head stores only key → hash, not a value; flipping deny↔allow deletes the old key
// and puts the new one in the same transaction.
func entitlementHeadKey(deny bool, name string) string {
	side := entitlementAllow
	if deny {
		side = entitlementDeny
	}
	return policyRowKey(kindEntitlement, side+"\x00"+name)
}

func parseEntitlementHeadKey(rk string) (deny bool, name string, ok bool) {
	kind, rest, cut := strings.Cut(rk, "\x00")
	if !cut || kind != kindEntitlement {
		return false, "", false
	}
	side, name, cut := strings.Cut(rest, "\x00")
	if !cut || name == "" {
		return false, "", false
	}
	switch side {
	case entitlementDeny:
		return true, name, true
	case entitlementAllow:
		return false, name, true
	default:
		return false, "", false
	}
}

func (s *Service) tableSK() []byte {
	_, sk, err := s.keys.keys()
	if err != nil {
		return nil
	}
	return sk
}

func (s *Service) initTables() {
	anchor := signedhead.FileAnchor{Dir: s.cfg.Dir}
	alert := func(msg string) { s.d.Log.Error(msg) }
	s.policy = &signedhead.Table{
		Name:    headPolicy,
		Bucket:  bucketMeta,
		HeadKey: headKey,
		SK:      s.tableSK,
		Anchor:  anchor,
		Alert:   alert,
	}
	s.items = &signedhead.Table{
		Name:    headItems,
		Bucket:  bucketItems,
		HeadKey: headKey,
		SK:      s.tableSK,
		Anchor:  anchor,
		Alert:   alert,
	}
	s.eligibility = &signedhead.Table{
		Name:    headEligibility,
		Bucket:  bucketEntitlements,
		HeadKey: headKey,
		SK:      s.tableSK,
		Anchor:  anchor,
		Alert:   alert,
	}
	s.grantHead = &signedhead.Table{
		Name:    headGrants,
		Bucket:  bucketGrants,
		HeadKey: headKey,
		SK:      s.tableSK,
		Anchor:  anchor,
		Alert:   alert,
	}
}

func (s *Service) initHeads() {
	if s.keys.sealed() {
		return
	}
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		if s.policy.Load(tx).Status == signedhead.StatusFailsafe {
			s.stickFailsafe(headPolicy)
		}
		if s.items.Load(tx).Status == signedhead.StatusFailsafe {
			s.stickFailsafe(headItems)
		}
		if s.eligibility.Load(tx).Status == signedhead.StatusFailsafe {
			s.stickFailsafe(headEligibility)
		}
		if s.grantHead.Load(tx).Status == signedhead.StatusFailsafe {
			s.stickFailsafe(headGrants)
		}
		return nil
	})
}

func (s *Service) stickFailsafe(name string) {
	s.headMu.Lock()
	already := false
	switch name {
	case headPolicy:
		already = s.policyFailsafe
		s.policyFailsafe = true
	case headItems:
		already = s.itemsFailsafe
		s.itemsFailsafe = true
	case headEligibility:
		already = s.eligibilityFailsafe
		s.eligibilityFailsafe = true
	case headGrants:
		already = s.grantsFailsafe
		s.grantsFailsafe = true
	}
	s.headMu.Unlock()
	if already {
		return
	}
	switch name {
	case headPolicy:
		s.d.Log.Error("the vault policy head is unusable; collections, tags and permissions are treated as the strictest")
		s.auditAfterTx(store.AuditEntry{Actor: "beacon", Via: "system", Action: "secrets_policy_head_failsafe",
			Detail: "the vault policy head is unusable; collections, tags and permissions are treated as the strictest"})
		if s.d.Bot != nil {
			_ = s.d.Bot.Send(s.ctx, s.t("The vault policy head is unusable; collections, tags and permissions are treated as the strictest."))
		}
	case headItems:
		s.d.Log.Error("the vault items head is unusable; items are treated as not found")
		s.auditAfterTx(store.AuditEntry{Actor: "beacon", Via: "system", Action: "secrets_items_head_failsafe",
			Detail: "the vault items head is unusable; items are treated as not found"})
		if s.d.Bot != nil {
			_ = s.d.Bot.Send(s.ctx, s.t("The vault items head is unusable; items are treated as not found."))
		}
	case headEligibility:
		s.d.Log.Error("the vault eligibility head is unusable; every entitlement is treated as deny-all")
		s.auditAfterTx(store.AuditEntry{Actor: "beacon", Via: "system", Action: "secrets_eligibility_head_failsafe",
			Detail: "the vault eligibility head is unusable; every entitlement is treated as deny-all"})
		if s.d.Bot != nil {
			_ = s.d.Bot.Send(s.ctx, s.t("The vault eligibility head is unusable; every entitlement is treated as deny-all."))
		}
	case headGrants:
		s.d.Log.Error("the vault grants head is unusable; no grant is treated as active")
		s.auditAfterTx(store.AuditEntry{Actor: "beacon", Via: "system", Action: "secrets_grants_head_failsafe",
			Detail: "the vault grants head is unusable; no grant is treated as active"})
		if s.d.Bot != nil {
			_ = s.d.Bot.Send(s.ctx, s.t("The vault grants head is unusable; no grant is treated as active."))
		}
	}
}

func (s *Service) loadPolicy(tx kv.Tx) signedhead.LoadResult {
	s.headMu.Lock()
	stuck := s.policyFailsafe
	s.headMu.Unlock()
	if stuck {
		return signedhead.LoadResult{Status: signedhead.StatusFailsafe}
	}
	res := s.policy.Load(tx)
	if res.Status == signedhead.StatusFailsafe {
		s.stickFailsafe(headPolicy)
	}
	return res
}

func (s *Service) loadItems(tx kv.Tx) signedhead.LoadResult {
	s.headMu.Lock()
	stuck := s.itemsFailsafe
	s.headMu.Unlock()
	if stuck {
		return signedhead.LoadResult{Status: signedhead.StatusFailsafe}
	}
	res := s.items.Load(tx)
	if res.Status == signedhead.StatusFailsafe {
		s.stickFailsafe(headItems)
	}
	return res
}

func (s *Service) policyAccept(tx kv.Tx, kind, key string, raw []byte) bool {
	res := s.loadPolicy(tx)
	rk := policyRowKey(kind, key)
	if res.Status == signedhead.StatusFailsafe || res.Status == signedhead.StatusEmpty {
		return false
	}
	if signedhead.Accept(res, rk, raw) {
		return true
	}
	if _, named := res.Head.Entries[rk]; named {
		s.reportHeadBreak(kind, key)
		return false
	}
	if len(raw) > 0 {
		s.reportUnnamed(kind, key)
	}
	return false
}

func (s *Service) policyNamed(tx kv.Tx, kind, key string) bool {
	res := s.loadPolicy(tx)
	if res.Status != signedhead.StatusOK && res.Status != signedhead.StatusCatchUp {
		return false
	}
	_, named := res.Head.Entries[policyRowKey(kind, key)]
	return named
}

func (s *Service) itemAccept(tx kv.Tx, id string, raw []byte) bool {
	res := s.loadItems(tx)
	if res.Status == signedhead.StatusFailsafe || res.Status == signedhead.StatusEmpty {
		return false
	}
	if signedhead.Accept(res, id, raw) {
		return true
	}
	if _, named := res.Head.Entries[id]; named {
		s.reportHeadBreak(kindItem, id)
		return false
	}
	if len(raw) > 0 {
		s.reportUnnamed(kindItem, id)
	}
	return false
}

func (s *Service) loadEligibility(tx kv.Tx) signedhead.LoadResult {
	s.headMu.Lock()
	stuck := s.eligibilityFailsafe
	s.headMu.Unlock()
	if stuck {
		return signedhead.LoadResult{Status: signedhead.StatusFailsafe}
	}
	res := s.eligibility.Load(tx)
	if res.Status == signedhead.StatusFailsafe {
		s.stickFailsafe(headEligibility)
	}
	return res
}

func (s *Service) loadGrants(tx kv.Tx) signedhead.LoadResult {
	s.headMu.Lock()
	stuck := s.grantsFailsafe
	s.headMu.Unlock()
	if stuck {
		return signedhead.LoadResult{Status: signedhead.StatusFailsafe}
	}
	res := s.grantHead.Load(tx)
	if res.Status == signedhead.StatusFailsafe {
		s.stickFailsafe(headGrants)
	}
	return res
}

func (s *Service) grantAccept(tx kv.Tx, id string, raw []byte) bool {
	res := s.loadGrants(tx)
	if res.Status == signedhead.StatusFailsafe || res.Status == signedhead.StatusEmpty {
		return false
	}
	if signedhead.Accept(res, id, raw) {
		return true
	}
	if _, named := res.Head.Entries[id]; named {
		s.reportHeadBreak(kindGrant, id)
		return false
	}
	if len(raw) > 0 {
		s.reportUnnamed(kindGrant, id)
	}
	return false
}

func (s *Service) reportHeadBreak(kind, key string) {
	label := kind + " " + key
	if !s.noteBad(label) {
		return
	}
	s.d.Log.Error("a policy row is missing, stale or bad and is treated as the strictest", "kind", kind, "row", key)
	s.auditAfterTx(store.AuditEntry{Actor: "beacon", Via: "system", Action: "secrets_policy_head",
		Detail: "a " + kind + " row is missing, stale or bad and is treated as the strictest"})
	if s.d.Bot != nil {
		switch kind {
		case kindItem:
			_ = s.d.Bot.Send(s.ctx, s.t("Vault item rows are missing, stale or bad and are treated as not found: %s.", label))
		case kindGrant:
			_ = s.d.Bot.Send(s.ctx, s.t("Vault grant rows are missing, stale or bad and are treated as not active: %s.", label))
		case kindEntitlement, kindAgentTags:
			_ = s.d.Bot.Send(s.ctx, s.t("Vault eligibility rows are missing, stale or bad and are treated as the strictest: %s.", label))
		default:
			_ = s.d.Bot.Send(s.ctx, s.t("Vault policy rows are missing, stale or bad and are treated as the strictest: %s.", label))
		}
	}
}

func (s *Service) reportUnnamed(kind, key string) {
	label := kind + " " + key + " (not in head)"
	if !s.noteBad(label) {
		return
	}
	s.d.Log.Error("a vault row is not named by the signed head and is ignored", "kind", kind, "row", key)
	s.auditAfterTx(store.AuditEntry{Actor: "beacon", Via: "system", Action: "secrets_policy_head",
		Detail: "a " + kind + " row is not named by the signed head and is ignored"})
	if s.d.Bot != nil {
		switch kind {
		case kindItem:
			_ = s.d.Bot.Send(s.ctx, s.t("Vault item rows are missing, stale or bad and are treated as not found: %s.", label))
		case kindGrant:
			_ = s.d.Bot.Send(s.ctx, s.t("Vault grant rows are missing, stale or bad and are treated as not active: %s.", label))
		case kindEntitlement, kindAgentTags:
			_ = s.d.Bot.Send(s.ctx, s.t("Vault eligibility rows are missing, stale or bad and are treated as the strictest: %s.", label))
		default:
			_ = s.d.Bot.Send(s.ctx, s.t("Vault policy rows are missing, stale or bad and are treated as the strictest: %s.", label))
		}
	}
}

// auditAfterTx writes an audit entry after the caller's database transaction has ended. The
// audit log shares the one write connection, so it is never written inside a kv Update.
func (s *Service) auditAfterTx(e store.AuditEntry) {
	go func() { _ = s.auditEntry(e) }()
}

func (s *Service) noteBad(label string) bool {
	s.badMu.Lock()
	defer s.badMu.Unlock()
	for _, b := range s.bad {
		if b == label {
			return false
		}
	}
	s.bad = append(s.bad, label)
	return true
}

func headErr(err error) error {
	if errors.Is(err, signedhead.ErrFailsafe) {
		return fmt.Errorf("%w: run beacon secret rebuild-head", ErrHead)
	}
	return err
}

func (s *Service) commitPolicy(tx kv.Tx, put map[string][]byte, del []string) (signedhead.Head, error) {
	s.headMu.Lock()
	stuck := s.policyFailsafe
	s.headMu.Unlock()
	if stuck {
		return signedhead.Head{}, fmt.Errorf("%w: run beacon secret rebuild-head", ErrHead)
	}
	h, err := s.policy.Commit(tx, put, del)
	return h, headErr(err)
}

func (s *Service) commitItems(tx kv.Tx, put map[string][]byte, del []string) (signedhead.Head, error) {
	s.headMu.Lock()
	stuck := s.itemsFailsafe
	s.headMu.Unlock()
	if stuck {
		return signedhead.Head{}, fmt.Errorf("%w: run beacon secret rebuild-head", ErrHead)
	}
	h, err := s.items.Commit(tx, put, del)
	return h, headErr(err)
}

func (s *Service) commitEligibility(tx kv.Tx, put map[string][]byte, del []string) (signedhead.Head, error) {
	s.headMu.Lock()
	stuck := s.eligibilityFailsafe
	s.headMu.Unlock()
	if stuck {
		return signedhead.Head{}, fmt.Errorf("%w: run beacon secret rebuild-head", ErrHead)
	}
	h, err := s.eligibility.Commit(tx, put, del)
	return h, headErr(err)
}

func (s *Service) commitGrants(tx kv.Tx, put map[string][]byte, del []string) (signedhead.Head, error) {
	s.headMu.Lock()
	stuck := s.grantsFailsafe
	s.headMu.Unlock()
	if stuck {
		return signedhead.Head{}, fmt.Errorf("%w: run beacon secret rebuild-head", ErrHead)
	}
	h, err := s.grantHead.Commit(tx, put, del)
	return h, headErr(err)
}

func (s *Service) afterHeads(heads ...signedhead.Head) {
	s.headMu.Lock()
	for _, h := range heads {
		switch h.Name {
		case headPolicy:
			s.policyFailsafe = false
		case headItems:
			s.itemsFailsafe = false
		case headEligibility:
			s.eligibilityFailsafe = false
		case headGrants:
			s.grantsFailsafe = false
		}
	}
	s.headMu.Unlock()
	for _, h := range heads {
		if h.Epoch == 0 {
			continue
		}
		switch h.Name {
		case headPolicy:
			if err := s.policy.AfterCommit(h); err != nil {
				s.d.Log.Error("the vault epochs file could not be written", "head", h.Name, "err", err)
			}
		case headItems:
			if err := s.items.AfterCommit(h); err != nil {
				s.d.Log.Error("the vault epochs file could not be written", "head", h.Name, "err", err)
			}
		case headEligibility:
			if err := s.eligibility.AfterCommit(h); err != nil {
				s.d.Log.Error("the vault epochs file could not be written", "head", h.Name, "err", err)
			}
		case headGrants:
			if err := s.grantHead.AfterCommit(h); err != nil {
				s.d.Log.Error("the vault epochs file could not be written", "head", h.Name, "err", err)
			}
		case "settings":
			if s.d.Settings != nil {
				s.d.Settings.AfterCommit(h)
			}
		}
	}
}

func putCollectionTx(tx kv.Tx, col Collection, sk []byte) ([]byte, error) {
	r := signedCollection{Collection: col}
	r.Sig = signRow(sk, col.signedParts()...)
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	if err := tx.Put(bucketCollections, []byte(col.ID), b); err != nil {
		return nil, err
	}
	return b, nil
}

func (s *Service) putItemTx(tx kv.Tx, item Item) (signedhead.Head, error) {
	sk, err := s.sk()
	if err != nil {
		return signedhead.Head{}, err
	}
	r := signedItem{Item: item}
	r.Sig = signRow(sk, item.signedParts()...)
	b, err := json.Marshal(r)
	if err != nil {
		return signedhead.Head{}, err
	}
	if err := tx.Put(bucketItems, []byte(item.ID), b); err != nil {
		return signedhead.Head{}, err
	}
	return s.commitItems(tx, map[string][]byte{item.ID: b}, nil)
}

func (s *Service) rootRotating() bool {
	s.rotRootMu.Lock()
	defer s.rotRootMu.Unlock()
	return s.rotatingRoot
}

func (s *Service) beginRootRotation() bool {
	s.rotRootMu.Lock()
	defer s.rotRootMu.Unlock()
	if s.rotatingRoot {
		return false
	}
	s.rotatingRoot = true
	return true
}

func (s *Service) endRootRotation() {
	s.rotRootMu.Lock()
	s.rotatingRoot = false
	s.rotRootMu.Unlock()
}

func (s *Service) refuseIfRotating() error {
	if s.rootRotating() {
		return fmt.Errorf("%w: the root key is being rotated", ErrConflict)
	}
	return nil
}

// RebuildHead is owner recovery: it scans the policy, item, eligibility and active-grant rows
// that still verify, writes a new signed head for each, and records the rebuild. Without it, a
// missing or bad head stays failsafe; Beacon does not rebuild on a later write.
func (s *Service) RebuildHead(who Actor) error {
	if s.keys.sealed() {
		return ErrSealed
	}
	if !who.Owner() {
		return fmt.Errorf("%w: rebuilding a signed head is the owner's recovery", ErrForbidden)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var policyH, itemsH, eligH, grantsH signedhead.Head
	err := s.d.Store.Storage().Update(func(tx kv.Tx) error {
		policyRows := map[string][]byte{}
		tx.Range(bucketCollections, kv.Range{}, func(k, v []byte) bool {
			var r signedCollection
			if json.Unmarshal(v, &r) != nil || string(k) != r.ID || !s.collectionVerifies(r) {
				return true
			}
			policyRows[policyRowKey(kindCollection, r.ID)] = append([]byte(nil), v...)
			return true
		})
		tx.Range(bucketTags, kv.Range{}, func(k, v []byte) bool {
			var r signedTag
			if json.Unmarshal(v, &r) != nil || string(k) != r.Name || !s.tagVerifies(r) {
				return true
			}
			policyRows[policyRowKey(kindTag, r.Name)] = append([]byte(nil), v...)
			return true
		})
		tx.Range(bucketPerms, kv.Range{}, func(k, v []byte) bool {
			var p Permission
			if json.Unmarshal(v, &p) != nil || string(k) != string(permRowKey(p.Collection, p.Subject)) || !s.permVerifies(p) {
				return true
			}
			policyRows[policyRowKey(kindPerm, p.Collection+"\x00"+p.Subject)] = append([]byte(nil), v...)
			return true
		})
		h, err := s.policy.Rebuild(tx, policyRows)
		if err != nil {
			return err
		}
		policyH = h
		itemRows := map[string][]byte{}
		tx.Range(bucketItems, kv.Range{}, func(k, v []byte) bool {
			if isHeadKey(k) {
				return true
			}
			var r signedItem
			if json.Unmarshal(v, &r) != nil || string(k) != r.ID || !s.itemVerifies(r) {
				return true
			}
			itemRows[r.ID] = append([]byte(nil), v...)
			return true
		})
		h, err = s.items.Rebuild(tx, itemRows)
		if err != nil {
			return err
		}
		itemsH = h
		eligRows := map[string][]byte{}
		tx.Range(bucketEntitlements, kv.Range{}, func(k, v []byte) bool {
			if isHeadKey(k) {
				return true
			}
			var e Entitlement
			if json.Unmarshal(v, &e) != nil || string(k) != e.Name || !s.entitlementVerifies(e) {
				return true
			}
			eligRows[entitlementHeadKey(e.Deny, e.Name)] = append([]byte(nil), v...)
			return true
		})
		tx.Range(bucketAgentTags, kv.Range{}, func(k, v []byte) bool {
			var t AgentTags
			if json.Unmarshal(v, &t) != nil || string(k) != t.Agent || !s.agentTagsVerify(t) {
				return true
			}
			eligRows[policyRowKey(kindAgentTags, t.Agent)] = append([]byte(nil), v...)
			return true
		})
		h, err = s.eligibility.Rebuild(tx, eligRows)
		if err != nil {
			return err
		}
		eligH = h
		grantRows := map[string][]byte{}
		tx.Range(bucketGrants, kv.Range{}, func(k, v []byte) bool {
			if isHeadKey(k) {
				return true
			}
			var g Grant
			if json.Unmarshal(v, &g) != nil || string(k) != g.ID || g.Status != GrantActive || !s.grantVerifies(g) {
				return true
			}
			grantRows[g.ID] = append([]byte(nil), v...)
			return true
		})
		h, err = s.grantHead.Rebuild(tx, grantRows)
		if err != nil {
			return err
		}
		grantsH = h
		return nil
	})
	if err != nil {
		return err
	}
	s.headMu.Lock()
	s.policyFailsafe, s.itemsFailsafe, s.eligibilityFailsafe, s.grantsFailsafe = false, false, false, false
	s.headMu.Unlock()
	s.afterHeads(policyH, itemsH, eligH, grantsH)
	_ = s.auditEntry(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: "secrets_head_rebuild",
		Detail: "rebuilt the vault policy, items, eligibility and grants heads from the rows that still verify"})
	if s.d.Bot != nil {
		_ = s.d.Bot.Send(s.ctx, s.t("%s rebuilt the vault signed heads via %s", who.Name, s.viaWords(who.Via)))
	}
	return nil
}

// resignNamedTx re-signs the rows the current verified head names that still verify under oldSK,
// then CommitFrom under newSK. Leftover unverified rows stay out of the head. A failsafe table
// stays failsafe: it is not rebuilt here.
func (s *Service) resignPolicyNamedTx(tx kv.Tx, oldSK, newSK []byte) (signedhead.Head, error) {
	res := s.policy.Load(tx)
	if res.Status == signedhead.StatusFailsafe {
		s.stickFailsafe(headPolicy)
		return signedhead.Head{}, nil
	}
	puts := map[string][]byte{}
	for rk := range res.Head.Entries {
		kind, key, ok := strings.Cut(rk, "\x00")
		if !ok {
			continue
		}
		var bucket []byte
		switch kind {
		case kindCollection:
			bucket = bucketCollections
		case kindTag:
			bucket = bucketTags
		case kindPerm:
			bucket = bucketPerms
		default:
			continue
		}
		v := tx.Get(bucket, []byte(key))
		if len(v) == 0 {
			continue
		}
		b, ok := resignPolicyBody(v, kind, oldSK, newSK)
		if !ok {
			continue
		}
		if err := tx.Put(bucket, []byte(key), b); err != nil {
			return signedhead.Head{}, err
		}
		puts[rk] = b
	}
	tbl := *s.policy
	tbl.SK = func() []byte { return newSK }
	h, err := tbl.CommitFrom(tx, res, puts, nil)
	return h, headErr(err)
}

func resignPolicyBody(v []byte, kind string, oldSK, newSK []byte) ([]byte, bool) {
	switch kind {
	case kindCollection:
		var r signedCollection
		if json.Unmarshal(v, &r) != nil || !verifyRow(oldSK, r.Sig, r.signedParts()...) {
			return nil, false
		}
		r.Sig = signRow(newSK, r.signedParts()...)
		b, err := json.Marshal(r)
		return b, err == nil
	case kindTag:
		var r signedTag
		if json.Unmarshal(v, &r) != nil || !verifyRow(oldSK, r.Sig, r.signedParts()...) {
			return nil, false
		}
		r.Sig = signRow(newSK, r.signedParts()...)
		b, err := json.Marshal(r)
		return b, err == nil
	case kindPerm:
		var r Permission
		if json.Unmarshal(v, &r) != nil || !verifyRow(oldSK, r.Sig, r.signedParts()...) {
			return nil, false
		}
		r.Sig = signRow(newSK, r.signedParts()...)
		b, err := json.Marshal(r)
		return b, err == nil
	}
	return nil, false
}

func (s *Service) resignItemsNamedTx(tx kv.Tx, oldSK, newSK []byte) (signedhead.Head, error) {
	res := s.items.Load(tx)
	if res.Status == signedhead.StatusFailsafe {
		s.stickFailsafe(headItems)
		return signedhead.Head{}, nil
	}
	puts := map[string][]byte{}
	for id := range res.Head.Entries {
		if id == string(headKey) {
			continue
		}
		v := tx.Get(bucketItems, []byte(id))
		if len(v) == 0 {
			continue
		}
		var r signedItem
		if json.Unmarshal(v, &r) != nil || !verifyRow(oldSK, r.Sig, r.signedParts()...) {
			continue
		}
		r.Sig = signRow(newSK, r.signedParts()...)
		b, err := json.Marshal(r)
		if err != nil {
			return signedhead.Head{}, err
		}
		if err := tx.Put(bucketItems, []byte(id), b); err != nil {
			return signedhead.Head{}, err
		}
		puts[id] = b
	}
	tbl := *s.items
	tbl.SK = func() []byte { return newSK }
	h, err := tbl.CommitFrom(tx, res, puts, nil)
	return h, headErr(err)
}

func (s *Service) resignEligibilityNamedTx(tx kv.Tx, oldSK, newSK []byte) (signedhead.Head, error) {
	res := s.eligibility.Load(tx)
	if res.Status == signedhead.StatusFailsafe {
		s.stickFailsafe(headEligibility)
		return signedhead.Head{}, nil
	}
	puts := map[string][]byte{}
	for rk := range res.Head.Entries {
		if deny, name, ok := parseEntitlementHeadKey(rk); ok {
			_ = deny
			v := tx.Get(bucketEntitlements, []byte(name))
			if len(v) == 0 {
				continue
			}
			var e Entitlement
			if json.Unmarshal(v, &e) != nil || !verifyRow(oldSK, e.Sig, e.signedParts()...) {
				continue
			}
			e.Sig = signRow(newSK, e.signedParts()...)
			b, err := json.Marshal(e)
			if err != nil {
				return signedhead.Head{}, err
			}
			if err := tx.Put(bucketEntitlements, []byte(name), b); err != nil {
				return signedhead.Head{}, err
			}
			puts[rk] = b
			continue
		}
		kind, key, ok := strings.Cut(rk, "\x00")
		if !ok || kind != kindAgentTags {
			continue
		}
		v := tx.Get(bucketAgentTags, []byte(key))
		if len(v) == 0 {
			continue
		}
		var t AgentTags
		if json.Unmarshal(v, &t) != nil || !verifyRow(oldSK, t.Sig, t.signedParts()...) {
			continue
		}
		t.Sig = signRow(newSK, t.signedParts()...)
		b, err := json.Marshal(t)
		if err != nil {
			return signedhead.Head{}, err
		}
		if err := tx.Put(bucketAgentTags, []byte(key), b); err != nil {
			return signedhead.Head{}, err
		}
		puts[rk] = b
	}
	tbl := *s.eligibility
	tbl.SK = func() []byte { return newSK }
	h, err := tbl.CommitFrom(tx, res, puts, nil)
	return h, headErr(err)
}

func (s *Service) resignGrantsNamedTx(tx kv.Tx, oldSK, newSK []byte) (signedhead.Head, error) {
	res := s.grantHead.Load(tx)
	if res.Status == signedhead.StatusFailsafe {
		s.stickFailsafe(headGrants)
		return signedhead.Head{}, nil
	}
	puts := map[string][]byte{}
	for id := range res.Head.Entries {
		if isHeadKey([]byte(id)) {
			continue
		}
		v := tx.Get(bucketGrants, []byte(id))
		if len(v) == 0 {
			continue
		}
		var g Grant
		if json.Unmarshal(v, &g) != nil || !verifyRow(oldSK, g.Sig, g.signedParts()...) {
			continue
		}
		g.Sig = signRow(newSK, g.signedParts()...)
		b, err := json.Marshal(g)
		if err != nil {
			return signedhead.Head{}, err
		}
		if err := tx.Put(bucketGrants, []byte(id), b); err != nil {
			return signedhead.Head{}, err
		}
		puts[id] = b
	}
	tbl := *s.grantHead
	tbl.SK = func() []byte { return newSK }
	h, err := tbl.CommitFrom(tx, res, puts, nil)
	return h, headErr(err)
}
