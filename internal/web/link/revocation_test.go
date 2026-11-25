package link

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRevocableSignerKeepsResourcesAndAliasesSeparate(t *testing.T) {
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.FixedZone("fixture", 8*60*60))
	key := bytes.Repeat([]byte{11}, KeySize)
	legacy, err := NewSigner(key, time.Hour, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	generations := map[resource]uint64{}
	s, err := NewRevocableSigner(key, time.Hour, func() time.Time { return now }, func(kind, id string) (uint64, error) {
		return generations[resource{kind, id}], nil
	})
	if err != nil {
		t.Fatal(err)
	}
	const id, other, episode = "incident-1", "incident-2", "0123456789abcdef"
	const get = http.MethodGet
	group, pin := AlertPath(id), IncidentPath(id, episode)
	oldAlert := "/alerts/" + id
	session, recording := SessionPath(id), RecordingPath(id)
	oldSession, oldRecording := "/sessions/"+id, "/sessions/"+id+"/recording"
	oldSig := legacy.Sign(get, oldSession)
	groupSig, pinSig := s.Sign(get, group), s.Sign(get, pin)
	oldAlertSig := legacy.Sign(get, oldAlert)
	otherSig := s.Sign(get, AlertPath(other))
	recSig := s.Sign(get, recording)
	if strings.Count(groupSig, ".") != 2 || strings.Count(oldSig, ".") != 1 {
		t.Fatalf("unexpected signature formats: fresh has %d parts, legacy has %d", strings.Count(groupSig, ".")+1, strings.Count(oldSig, ".")+1)
	}
	for path, sig := range map[string]string{group: groupSig, pin: pinSig, oldAlert: oldAlertSig, oldSession: oldSig,
		oldRecording: legacy.Sign(get, oldRecording), recording: recSig, AlertPath(other): otherSig} {
		g, ok := s.VerifyGrant(get, path, sig)
		if !ok || !g.Until.Equal(now.Add(time.Hour)) || g.Until.Location() != time.UTC {
			t.Fatalf("generation zero did not verify at %s: %v, %s", path, ok, g.Until)
		}
	}

	grant, _ := s.VerifyGrant(get, oldSession, oldSig)
	grant.Until = now.Add(24 * time.Hour)
	moved, derived := s.Derive(get, session, grant), s.Derive(get, recording, grant)
	for path, sig := range map[string]string{session: moved, recording: derived} {
		if until, ok := s.Verify(get, path, sig); !ok || !until.Equal(now.Add(time.Hour)) {
			t.Fatalf("derivation did not preserve the authenticated expiry at %s: %v, %s", path, ok, until)
		}
	}
	g, _ := s.VerifyGrant(get, oldAlert, oldAlertSig)
	if sig := s.Derive(get, group, g); sig == "" {
		t.Fatal("the historical group URL could not move to its current URL")
	}
	for path, from := range map[string]Grant{
		group: grant, SessionPath(other): grant, pin: g, "/unrelated": grant,
	} {
		if s.Derive(get, path, from) != "" {
			t.Errorf("grant changed its destination to %s", path)
		}
	}
	recordingGrant, _ := s.VerifyGrant(get, recording, recSig)
	if s.Derive(get, session, recordingGrant) != "" || s.Derive(http.MethodPost, recording, grant) != "" ||
		s.Derive(get, recording, Grant{Until: now.Add(time.Hour)}) != "" {
		t.Fatal("a recording, another method or an unverified grant could derive a page")
	}
	if _, ok := s.Verify(get, oldSession, moved); ok {
		t.Fatal("a canonical signature was accepted on its legacy path")
	}

	generations[resource{"alert", id}] = 1
	for path, sig := range map[string]string{group: groupSig, pin: pinSig, oldAlert: oldAlertSig} {
		if _, ok := s.Verify(get, path, sig); ok {
			t.Fatalf("an old group or episode link remained valid at %s", path)
		}
	}
	if _, ok := s.Verify(get, AlertPath(other), otherSig); !ok {
		t.Fatal("a group revocation affected another group")
	}
	if _, ok := s.Verify(get, session, moved); !ok {
		t.Fatal("a group revocation affected a session with the same ID")
	}
	first := s.Sign(get, pin)
	if _, ok := s.Verify(get, pin, first); !ok {
		t.Fatal("a newly issued episode link failed")
	}
	if _, ok := s.Verify(get, pin, strings.Replace(first, ".1.", ".0.", 1)); ok {
		t.Fatal("generation was not authenticated")
	}
	generations[resource{"alert", id}] = 2
	if _, ok := s.Verify(get, pin, first); ok {
		t.Fatal("the second revocation did not invalidate links issued after the first")
	}

	generations[resource{"session", id}] = 1
	if s.Derive(get, recording, grant) != "" || s.Derive(get, session, grant) != "" {
		t.Fatal("a verified grant survived a revocation and issued new links")
	}
	for path, sig := range map[string]string{session: moved, recording: derived, oldSession: oldSig, oldRecording: legacy.Sign(get, oldRecording)} {
		if _, ok := s.Verify(get, path, sig); ok {
			t.Fatalf("a previous replay or recording link remained valid at %s", path)
		}
	}
	fresh := s.Sign(get, session)
	freshGrant, ok := s.VerifyGrant(get, session, fresh)
	if !ok || s.Derive(get, recording, freshGrant) == "" {
		t.Fatal("current-generation replay could not derive a recording")
	}
}

func TestRevocableSignerRefusesUnavailableOrUnknownGenerations(t *testing.T) {
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	key := bytes.Repeat([]byte{12}, KeySize)
	const get = http.MethodGet
	if _, err := NewRevocableSigner(key, time.Hour, nil, nil); err == nil {
		t.Fatal("the revocable signer was configured without a generation lookup")
	}
	lookups := 0
	unavailable := false
	s, err := NewRevocableSigner(key, time.Hour, func() time.Time { return now }, func(kind, id string) (uint64, error) {
		lookups++
		if unavailable {
			return 0, errors.New("fixture storage is unavailable")
		}
		return 0, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	path := SessionPath("a")
	sig := s.Sign(get, path)
	g, ok := s.VerifyGrant(get, path, sig)
	if !ok {
		t.Fatal("the test's original grant was invalid")
	}
	checked := lookups
	bad := strings.Replace(sig, ".0.", ".00.", 1)
	if _, ok := s.Verify(get, path, bad); ok || lookups != checked {
		t.Fatal("an invalid signature was accepted or queried storage")
	}
	unavailable = true
	if _, ok := s.Verify(get, path, sig); ok || s.Sign(get, path) != "" || s.Derive(get, RecordingPath("a"), g) != "" {
		t.Fatal("lookup failure allowed verification, new issuance or derivation")
	}
	if p := (&Pages{Signer: s, PublicURL: "https://beacon.example.com"}).Session("a"); p != "" {
		t.Fatal("an unusable URL was emitted after lookup failure")
	}
	unavailable = false
	for _, path := range []string{"/unrelated", "/console/alerts/a/ack", "/console/sessions/a/recording/",
		"/console/alerts/a/episodes/0123456789abcdeg", "sessions/a", "/alerts/a/episodes/0123456789abcdef"} {
		if sig := s.Sign(get, path); sig != "" {
			t.Fatalf("unsupported path received a revocable signature: %s", path)
		}
	}
	standalone := newSigner(t, &now)
	path = "/another/use"
	sig = standalone.Sign(get, path)
	if _, ok := standalone.Verify(get, path, sig); !ok {
		t.Fatal("standalone exact-path signing lost its existing behaviour")
	}
}
