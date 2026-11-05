package secrets

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/store"
)

// Actor is who does something to the vault: a person (Role is their global role, and an owner holds
// manage on every collection), or the system. Name is the account's name; Via is how — api, mcp,
// cli or console.
type Actor struct {
	Name string
	Via  string
	Role string // viewer, oncall, operator or owner; empty for the system
}

// Person reports whether the actor is a person.
func (a Actor) Person() bool { return a.Name != "" && a.Role != "" }

// Owner reports whether the actor owns the deployment.
func (a Actor) Owner() bool { return a.Role == "owner" }

// ActorForUser resolves an account's name to the vault's actor (with the account's role), for the
// command line, where a person names themselves and re-enters their password to view values.
func (s *Service) ActorForUser(name string) (Actor, bool) {
	acc, ok := s.d.Store.Account(name)
	if !ok || acc.Disabled {
		return Actor{}, false
	}
	return Actor{Name: acc.Username, Via: "cli", Role: acc.Role}, true
}

// ---- collections

// nameRe is what a collection may be called: letters, digits and -_. and spaces (the reference
// syntax addresses things by name when the name is this shape, by ID otherwise).
var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 \-_.]{0,63}$`)

// tagRe is a tag's shape: lower-case, layered with slashes.
var tagRe = regexp.MustCompile(`^[a-z0-9][a-z0-9 _\-]*(/[a-z0-9][a-z0-9 _\-]*)*$`)

// Collections lists the collections by name.
func (s *Service) Collections() []Collection {
	var out []Collection
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		tx.Range(bucketCollections, kv.Range{}, func(k, v []byte) bool {
			var r signedCollection
			if json.Unmarshal(v, &r) == nil && string(k) == r.ID && s.collectionLive(tx, r) {
				out = append(out, r.Collection)
			}
			return true
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// CollectionsFor lists the collections the actor may describe.
func (s *Service) CollectionsFor(who Actor) []Collection {
	var out []Collection
	for _, col := range s.Collections() {
		if s.May(who, col.ID, PermDescribe) {
			out = append(out, col)
		}
	}
	return out
}

// Collection finds a collection by its name or its ID.
func (s *Service) Collection(nameOrID string) (Collection, bool) {
	var out Collection
	found := false
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		tx.Range(bucketCollections, kv.Range{}, func(k, v []byte) bool {
			var r signedCollection
			if json.Unmarshal(v, &r) != nil || string(k) != r.ID || !s.collectionLive(tx, r) {
				return true
			}
			if r.Name == nameOrID || r.ID == nameOrID {
				out, found = r.Collection, true
				return false
			}
			return true
		})
		return nil
	})
	return out, found
}

// collectionVerifies is the signature check a collection row passes to be served at all.
func (s *Service) collectionVerifies(r signedCollection) bool {
	sk, err := s.sk()
	if err != nil {
		return false // sealed: nothing is served, which is the safe side
	}
	return verifyPolicy(sk, r.Sig, r.signedParts()...)
}

// collectionLive is the signature check plus the signed head: a row the head does not name, or
// whose contents no longer match it, is not served.
func (s *Service) collectionLive(tx kv.Tx, r signedCollection) bool {
	if !s.collectionVerifies(r) {
		return false
	}
	ok, named := s.headMatch(tx, kindCollection, r.ID, r.signedParts(), r.Level)
	if !named {
		return false
	}
	return ok
}

// checkActions checks an action restriction (describe, read, use).
func checkActions(actions []string) error {
	for _, a := range actions {
		if !ValidAction(a) {
			return fmt.Errorf("%w: an action is one of %s", ErrBadInput, joinStrings(Actions))
		}
	}
	return nil
}

// CreateCollection makes a collection with its first key version. Only the deployment's owner (or
// whoever the wiring lets act for the system) makes collections.
func (s *Service) CreateCollection(who Actor, name, description, level string, actions []string) (Collection, error) {
	if s.keys.sealed() {
		return Collection{}, ErrSealed
	}
	if hasNUL(name, description, level) {
		return Collection{}, fmt.Errorf("%w: a value may not hold a NUL", ErrBadInput)
	}
	if !nameRe.MatchString(name) {
		return Collection{}, fmt.Errorf("%w: a collection's name is up to 64 letters, digits, spaces, - _ and ., starting with a letter or digit", ErrBadInput)
	}
	if level == "" {
		level = LevelReview
	}
	if !ValidLevel(level) {
		return Collection{}, fmt.Errorf("%w: a level is one of %s", ErrBadInput, joinStrings(Levels))
	}
	if err := checkActions(actions); err != nil {
		return Collection{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := newID()
	if err != nil {
		return Collection{}, err
	}
	col := Collection{ID: id, Name: name, Description: description, Level: level, Actions: compact(actions), KeyVersion: 1,
		Created: s.now(), CreatedBy: who.Name} // the first key version arrives with the row
	sk, err := s.sk()
	if err != nil {
		return Collection{}, err
	}
	err = s.d.Store.Storage().Update(func(tx kv.Tx) error {
		if findCollectionByName(tx, name) != nil {
			return fmt.Errorf("%w: a collection is called %q already", ErrExists, name)
		}
		if err := putCollectionTx(tx, col, sk); err != nil {
			return err
		}
		return writePolicyHeadTx(tx, sk)
	})
	if err != nil {
		return Collection{}, err
	}
	if err := s.newCollectionKey(id); err != nil {
		return Collection{}, err
	}
	s.echo(who, "created the collection", name, "secrets_collection_create")
	return col, nil
}

// UpdateCollection changes a collection's description, default level or action restriction; the
// level and the actions are policy, so they need manage.
func (s *Service) UpdateCollection(who Actor, nameOrID, description string, level string, actions []string) (Collection, error) {
	if s.keys.sealed() {
		return Collection{}, ErrSealed
	}
	if hasNUL(nameOrID, description, level) {
		return Collection{}, fmt.Errorf("%w: a value may not hold a NUL", ErrBadInput)
	}
	if level != "" && !ValidLevel(level) {
		return Collection{}, fmt.Errorf("%w: a level is one of %s", ErrBadInput, joinStrings(Levels))
	}
	if err := checkActions(actions); err != nil {
		return Collection{}, err
	}
	var out Collection
	var levelChanged bool
	var err error
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		sk, lockErr := s.sk()
		if lockErr != nil {
			err = lockErr
			return
		}
		err = s.d.Store.Storage().Update(func(tx kv.Tx) error {
			var r *signedCollection
			tx.Range(bucketCollections, kv.Range{}, func(_, v []byte) bool {
				var c signedCollection
				if json.Unmarshal(v, &c) == nil && (c.Name == nameOrID || c.ID == nameOrID) {
					cc := c
					r = &cc
					return false
				}
				return true
			})
			if r == nil {
				return fmt.Errorf("%w: no collection %q", ErrNotFound, nameOrID)
			}
			if !s.collectionVerifies(*r) {
				return fmt.Errorf("%w: the collection's row does not verify", ErrNotFound)
			}
			if !s.mayManageTx(tx, who, r.ID) {
				return fmt.Errorf("%w: changing a collection's policy needs manage", ErrForbidden)
			}
			if description != "" {
				r.Description = description
			}
			if level != "" && level != r.Level {
				levelChanged = true
				r.Level = level
			}
			switch {
			case len(actions) == 1 && actions[0] == "-":
				if r.Actions != nil {
					levelChanged = true // the policy around the items changed
				}
				r.Actions = nil
			case len(actions) > 0:
				r.Actions = compact(actions)
			}
			out = r.Collection
			if err := putCollectionTx(tx, r.Collection, sk); err != nil {
				return err
			}
			return writePolicyHeadTx(tx, sk)
		})
	}()
	if err != nil {
		return Collection{}, err
	}
	s.echo(who, "changed the collection", out.Name, "secrets_collection_update")
	if levelChanged {
		s.reassessGrants() // outside the lock: it takes the lock itself per grant
	}
	return out, nil
}

func putCollectionTx(tx kv.Tx, col Collection, sk []byte) error {
	r := signedCollection{Collection: col}
	r.Sig = signRow(sk, col.signedParts()...)
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return tx.Put(bucketCollections, []byte(col.ID), b)
}

func findCollectionByName(tx kv.Tx, name string) []byte {
	var id []byte
	tx.Range(bucketCollections, kv.Range{}, func(k, v []byte) bool {
		var c signedCollection
		if json.Unmarshal(v, &c) == nil && c.Name == name {
			id = append([]byte(nil), k...)
			return false
		}
		return true
	})
	return id
}

// ---- tags

// Tags lists the tags by name.
func (s *Service) Tags() []Tag {
	var out []Tag
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		tx.Range(bucketTags, kv.Range{}, func(_, v []byte) bool {
			var r signedTag
			if json.Unmarshal(v, &r) == nil && s.tagLive(tx, r) {
				out = append(out, r.Tag)
			}
			return true
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Service) tagVerifies(r signedTag) bool {
	sk, err := s.sk()
	if err != nil {
		return false
	}
	return verifyPolicy(sk, r.Sig, r.signedParts()...)
}

func (s *Service) tagLive(tx kv.Tx, r signedTag) bool {
	if !s.tagVerifies(r) {
		return false
	}
	ok, named := s.headMatch(tx, kindTag, r.Name, r.signedParts(), r.Level)
	if !named {
		return false
	}
	return ok
}

func (s *Service) tagByName(name string) (Tag, bool) {
	var out Tag
	ok := false
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		out, ok = s.tagByNameTx(tx, name)
		return nil
	})
	return out, ok
}

func (s *Service) tagByNameTx(tx kv.Tx, name string) (Tag, bool) {
	v := tx.Get(bucketTags, []byte(name))
	var r signedTag
	if v == nil || json.Unmarshal(v, &r) != nil || r.Name != name || !s.tagLive(tx, r) {
		return Tag{}, false
	}
	return r.Tag, true
}

// tagStatus is how a name on an item counts toward the approval level: a policy tag the head
// names and that matches, a broken row the head names (strictest), or a label the head does not.
func (s *Service) tagStatus(name string) (level string, broken, label bool) {
	var outLevel string
	var isBroken, isLabel bool
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		sk, err := s.sk()
		if err != nil {
			isBroken = true
			return err
		}
		h, headOK := loadPolicyHead(tx, sk)
		v := tx.Get(bucketTags, []byte(name))
		var r signedTag
		live := v != nil && json.Unmarshal(v, &r) == nil && r.Name == name && s.tagVerifies(r)
		if !headOK {
			if policyHeadPresent(tx) || anyPolicyTx(tx) {
				if live {
					s.reportHeadBreak(kindTag, name)
					isBroken = true
				} else {
					isLabel = true
				}
				return nil
			}
			if live {
				outLevel = r.Level
				return nil
			}
			isLabel = true
			return nil
		}
		ent, named := h.lookup(kindTag, name)
		if !named {
			isLabel = true
			return nil
		}
		if !live || ent.Hash != partsHash(r.signedParts()) || ent.Value != r.Level {
			s.reportHeadBreak(kindTag, name)
			isBroken = true
			return nil
		}
		outLevel = r.Level
		return nil
	})
	return outLevel, isBroken, isLabel
}

// SetTag adds or changes a tag. A tag's level is policy (it makes every item wearing the tag at
// least that strict), so this needs an owner's hand, and every change is recorded. Its actions,
// when set, narrow every item wearing it.
func (s *Service) SetTag(who Actor, name, level, description string, actions []string) (Tag, error) {
	if s.keys.sealed() {
		return Tag{}, ErrSealed
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if hasNUL(name, level, description) {
		return Tag{}, fmt.Errorf("%w: a value may not hold a NUL", ErrBadInput)
	}
	if !tagRe.MatchString(name) {
		return Tag{}, fmt.Errorf("%w: a tag is lower-case, layered with / (env/prod, service/dns)", ErrBadInput)
	}
	if level != "" && !ValidLevel(level) {
		return Tag{}, fmt.Errorf("%w: a level is one of %s", ErrBadInput, joinStrings(Levels))
	}
	if err := checkActions(actions); err != nil {
		return Tag{}, err
	}
	if !who.Owner() {
		return Tag{}, fmt.Errorf("%w: tags change what items they touch need, which only the owner does", ErrForbidden)
	}
	var err error
	var old Tag
	var existed bool
	var tag Tag
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		sk, lockErr := s.sk()
		if lockErr != nil {
			err = lockErr
			return
		}
		old, existed = s.tagByName(name)
		tag = Tag{Name: name, Level: level, Description: description}
		switch {
		case len(actions) == 1 && actions[0] == "-":
			tag.Actions = nil // "-" clears the restriction
		case len(actions) > 0:
			tag.Actions = compact(actions)
		case existed:
			tag.Actions = old.Actions // nothing named: the restriction stays
		}
		if existed {
			tag.Created = old.Created
		} else {
			tag.Created = s.now()
		}
		err = s.d.Store.Storage().Update(func(tx kv.Tx) error {
			r := signedTag{Tag: tag}
			r.Sig = signRow(sk, tag.signedParts()...)
			b, mErr := json.Marshal(r)
			if mErr != nil {
				return mErr
			}
			if err := tx.Put(bucketTags, []byte(tag.Name), b); err != nil {
				return err
			}
			return writePolicyHeadTx(tx, sk)
		})
	}()
	if err != nil {
		return Tag{}, err
	}
	s.echo(who, "set the tag", name, "secrets_tag_set")
	if existed && (old.Level != tag.Level || !equalStrings(old.Actions, tag.Actions)) {
		s.reassessGrants() // outside the lock: it takes the lock itself per grant
	}
	return tag, nil
}

// equalStrings says whether two lists hold the same strings.
func equalStrings(a, b []string) bool { return slices.Equal(a, b) }

// DeleteTag removes a tag; items wearing it keep the string but lose the level it carried.
func (s *Service) DeleteTag(who Actor, name string) error {
	if s.keys.sealed() {
		return ErrSealed
	}
	if !who.Owner() {
		return fmt.Errorf("%w: tags change what items they touch need, which only the owner does", ErrForbidden)
	}
	var err error
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		sk, lockErr := s.sk()
		if lockErr != nil {
			err = lockErr
			return
		}
		err = s.d.Store.Storage().Update(func(tx kv.Tx) error {
			if tx.Get(bucketTags, []byte(name)) == nil {
				return fmt.Errorf("%w: no tag %q", ErrNotFound, name)
			}
			if err := tx.Delete(bucketTags, []byte(name)); err != nil {
				return err
			}
			return writePolicyHeadTx(tx, sk, policyRowKey(kindTag, name))
		})
	}()
	if err != nil {
		return err
	}
	s.echo(who, "removed the tag", name, "secrets_tag_delete")
	s.reassessGrants()
	return nil
}

// EffectiveLevel is an item's approval level: what the item says when it says anything, else the
// strictest of the collection's default and every tag the item wears.
func (s *Service) EffectiveLevel(item Item) string {
	if item.Level != "" {
		return item.Level
	}
	col, ok := s.Collection(item.Collection)
	if !ok {
		return LevelNever // a missing or unverifiable collection does not fall back to auto
	}
	level := col.Level
	if level == "" {
		level = LevelNever
	}
	for _, t := range item.Tags {
		tagLevel, broken, label := s.tagStatus(t)
		if broken {
			return LevelNever
		}
		if label {
			continue // a name the head does not record as policy is a label
		}
		if tagLevel != "" {
			level = StrictestLevel(level, tagLevel)
		}
	}
	return level
}

// ---- permissions

// SetPermission grants a person or a role a permission over a collection. Perm empty removes the
// row. Needs manage over the collection (an owner holds it everywhere).
func (s *Service) SetPermission(who Actor, collection, subject, perm, reason string) error {
	if s.keys.sealed() {
		return ErrSealed
	}
	col, ok := s.Collection(collection)
	if !ok {
		return fmt.Errorf("%w: no collection %q", ErrNotFound, collection)
	}
	if hasNUL(collection, subject, perm, reason) {
		return fmt.Errorf("%w: a value may not hold a NUL", ErrBadInput)
	}
	if perm != "" && !ValidPerm(perm) {
		return fmt.Errorf("%w: a permission is one of %s", ErrBadInput, joinStrings(Perms))
	}
	if err := validSubject(subject); err != nil {
		return err
	}
	if !s.May(who, col.ID, PermManage) {
		return fmt.Errorf("%w: managing a collection's people needs manage", ErrForbidden)
	}
	var err error
	func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		sk, lockErr := s.sk()
		if lockErr != nil {
			err = lockErr
			return
		}
		err = s.d.Store.Storage().Update(func(tx kv.Tx) error {
			if perm == "" {
				if err := tx.Delete(bucketPerms, permRowKey(col.ID, subject)); err != nil {
					return err
				}
				return writePolicyHeadTx(tx, sk, policyRowKey(kindPerm, col.ID+"\x00"+subject))
			}
			p := Permission{Collection: col.ID, Subject: subject, Perm: perm,
				Actor: who.Name, Via: who.Via, At: s.now(), Reason: reason}
			p.Sig = signRow(sk, p.signedParts()...)
			b, err := json.Marshal(p)
			if err != nil {
				return err
			}
			if err := tx.Put(bucketPerms, permRowKey(col.ID, subject), b); err != nil {
				return err
			}
			return writePolicyHeadTx(tx, sk)
		})
	}()
	if err != nil {
		return err
	}
	s.echo(who, "set who may use the collection", col.Name+" ("+subject+" → "+perm+")", "secrets_permission_set")
	s.reassessGrants() // a permission change may leave owners without access they had
	return nil
}

// Permissions lists a collection's permission rows, verified ones only.
func (s *Service) Permissions(collection string) []Permission {
	col, ok := s.Collection(collection)
	if !ok {
		return nil
	}
	var out []Permission
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		prefix := []byte(col.ID + "\x00")
		tx.Range(bucketPerms, kv.Range{Prefix: prefix}, func(k, v []byte) bool {
			var p Permission
			if json.Unmarshal(v, &p) == nil && s.permLive(tx, k, p) {
				out = append(out, p)
			}
			return true
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Subject < out[j].Subject })
	return out
}

func (s *Service) permVerifies(p Permission) bool {
	sk, err := s.sk()
	if err != nil {
		return false
	}
	return verifyPolicy(sk, p.Sig, p.signedParts()...)
}

func (s *Service) permLive(tx kv.Tx, key []byte, p Permission) bool {
	if string(key) != string(permRowKey(p.Collection, p.Subject)) || !s.permVerifies(p) {
		return false
	}
	ok, named := s.headMatch(tx, kindPerm, p.Collection+"\x00"+p.Subject, p.signedParts(), p.Perm)
	if !named {
		return false
	}
	return ok
}

func permRowKey(collection, subject string) []byte { return []byte(collection + "\x00" + subject) }

// validSubject checks a permission row's subject: user:NAME or role:one of the four.
func validSubject(subject string) error {
	if role, ok := strings.CutPrefix(subject, "role:"); ok {
		if !store.ValidRole(role) {
			return fmt.Errorf("%w: a role is one of %s", ErrBadInput, joinStrings(store.Roles))
		}
		return nil
	}
	if name, ok := strings.CutPrefix(subject, permSubjectPrefix); ok {
		if name == "" {
			return fmt.Errorf("%w: the subject names the account", ErrBadInput)
		}
		return nil
	}
	return fmt.Errorf("%w: a subject is user:NAME or role:viewer|oncall|operator|owner", ErrBadInput)
}

// May reports whether the actor holds perm over the collection. An owner holds manage everywhere;
// anyone else takes the best of their own row and their role's row; a row that fails its signature
// is not there, which is the safe side.
func (s *Service) May(who Actor, collectionID, perm string) bool {
	have, ok := s.permOf(who, collectionID)
	return ok && PermAtLeast(have, perm)
}

// permOf is the actor's effective permission over a collection.
func (s *Service) permOf(who Actor, collectionID string) (string, bool) {
	if who.Owner() {
		return PermManage, true
	}
	if !who.Person() {
		return "", false
	}
	// sealed: the rows cannot be verified, so nobody is let in
	if s.keys.sealed() {
		return "", false
	}
	best, found := "", false
	_ = s.d.Store.Storage().View(func(tx kv.Tx) error {
		for _, subject := range []string{permSubjectPrefix + who.Name, "role:" + who.Role} {
			key := permRowKey(collectionID, subject)
			v := tx.Get(bucketPerms, key)
			if v == nil {
				if _, named := s.headMatch(tx, kindPerm, collectionID+"\x00"+subject, nil, ""); named {
					s.reportHeadBreak(kindPerm, collectionID+"\x00"+subject)
				}
				continue
			}
			var p Permission
			if json.Unmarshal(v, &p) != nil || p.Collection != collectionID || p.Subject != subject || !s.permLive(tx, key, p) {
				continue // a row that does not verify, that was copied, or that the head does not name, is not there
			}
			if !found || permRank[p.Perm] > permRank[best] {
				best, found = p.Perm, true
			}
		}
		return nil
	})
	return best, found
}

// mayManageTx is May inside a transaction, for the paths that already hold one.
func (s *Service) mayManageTx(tx kv.Tx, who Actor, collectionID string) bool {
	if who.Owner() {
		return true
	}
	if !who.Person() || s.keys.sealed() {
		return false
	}
	for _, subject := range []string{permSubjectPrefix + who.Name, "role:" + who.Role} {
		key := permRowKey(collectionID, subject)
		v := tx.Get(bucketPerms, key)
		if v == nil {
			continue
		}
		var p Permission
		if json.Unmarshal(v, &p) == nil && p.Collection == collectionID && p.Subject == subject &&
			s.permLive(tx, key, p) && PermAtLeast(p.Perm, PermManage) {
			return true
		}
	}
	return false
}

// WhoCanGet lists who may read a collection's values: the owner, the people and roles with read or
// more. For the item page's "who can get it".
func (s *Service) WhoCanGet(collection string) []string {
	if _, ok := s.Collection(collection); !ok {
		return nil
	}
	col, _ := s.Collection(collection)
	out := []string{}
	seen := map[string]bool{}
	for _, p := range s.Permissions(col.ID) {
		if PermAtLeast(p.Perm, PermRead) && !seen[p.Subject] {
			seen[p.Subject] = true
			out = append(out, p.Subject)
		}
	}
	sort.Strings(out)
	return out
}

// echo writes an audit entry and tells the alert chat what changed in the vault. Values never
// appear: the entries say what was done, by whom and why, as everywhere else in Beacon.
func (s *Service) echo(who Actor, verb, target, action string) {
	detail := verb + " " + target
	_ = s.auditEntry(store.AuditEntry{Actor: who.Name, Via: who.Via, Action: action, Target: target, Detail: detail})
	if s.d.Bot != nil {
		_ = s.d.Bot.Send(s.ctx, s.t("%s %s \"%s\" via %s", who.Name, verb, target, s.viaWords(who.Via)))
	}
}

func (s *Service) viaWords(via string) string {
	switch via {
	case "cli":
		return s.t("the command line")
	case "console":
		return s.t("the console")
	case "mcp":
		return "MCP"
	default:
		return s.t("the API")
	}
}
