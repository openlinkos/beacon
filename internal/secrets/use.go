package secrets

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/openlinkos/beacon/internal/events"
	"github.com/openlinkos/beacon/internal/kv"
	"github.com/openlinkos/beacon/internal/signedhead"
	"github.com/openlinkos/beacon/internal/store"
)

// credentialClient follows the HTTPS use path's no-redirect setting. Destinations are explicitly
// listed by the deployment and only an owner can bind an item to one, with no proxy involved.
func credentialClient(roots *x509.CertPool) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: 4 * time.Second}).DialContext,
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
			TLSHandshakeTimeout: 4 * time.Second,
			IdleConnTimeout:     30 * time.Second,
			MaxIdleConns:        8,
		},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       8 * time.Second,
	}
}

type Result struct {
	Status int `json:"status"`
}

// Use sends one fixed HTTPS request. As in the vault's original use path, an audit record must
// commit before the value can be unsealed. No service headers, response body or raw error is passed
// back to the caller.
func (s *Service) Use(ctx context.Context, p store.Principal, via, id string) (Result, error) {
	if !idRE.MatchString(id) {
		return Result{}, ErrInput
	}
	r, err := s.recordUse(p, via, id)
	if err != nil {
		return Result{}, err
	}
	s.tell(ctx, p, via, "credential.use", "", id)

	plain, err := unseal(s.aead, r.Ciphertext, rowContext(r.Credential))
	if err != nil {
		return Result{}, ErrState
	}
	defer clear(plain)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.Endpoint, nil)
	if err != nil {
		return Result{}, ErrState
	}
	req.Header.Set("Authorization", "Bearer "+string(plain))
	res, err := s.http.Do(req)
	if err != nil {
		return Result{}, ErrUpstream
	}
	_ = res.Body.Close()
	return Result{Status: res.StatusCode}, nil
}

func (s *Service) recordUse(p store.Principal, via, id string) (row, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.repairAnchor(); err != nil {
		return row{}, err
	}
	var r row
	err := s.d.Store.Storage().UpdateBoth(func(tx kv.Tx, raw *sql.Tx) error {
		current, a, ok := s.d.Store.TeamPrincipal(tx, p)
		owner := CanManage(p) && CanManage(current)
		if !ok || (!owner && (current.Agent == "" || !current.Has(UseScope) || !p.Has(UseScope))) {
			return ErrAccess
		}
		h := s.loadHead(tx)
		if h.Status == signedhead.StatusFailsafe {
			return ErrState
		}
		data := tx.Get(bucket, []byte(id))
		if !signedhead.Accept(h, id, data) || json.Unmarshal(data, &r) != nil || r.ID != id {
			return ErrAccess
		}
		if _, ok := s.target(r.Endpoint); !ok {
			return ErrAccess
		}
		if !owner {
			if _, ok := s.accessible(r, a); !ok || a.Owner != current.User {
				return ErrAccess
			}
		}
		e := events.Event{Actor: current.Name(), Via: via, Action: "credential.use", Object: id, Detail: "authorised an HTTPS request"}
		if current.Agent != "" {
			e.OnBehalf = current.User
		}
		if err := s.d.Store.AppendAuditTx(raw, e); err != nil {
			return err
		}
		return s.d.Store.UsesLog().AppendTx(raw, &e)
	})
	return r, err
}
