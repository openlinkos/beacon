package vm

import "net/http"

// BasicAuth sends username and password with every request through next (http.DefaultTransport when
// nil), in place of any Authorization the request had: the credentials VictoriaMetrics' -httpAuth.*
// flags ask for. With no username it is next itself.
func BasicAuth(next http.RoundTripper, username, password string) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	if username == "" {
		return next
	}
	return basicAuth{next: next, username: username, password: password}
}

type basicAuth struct {
	next               http.RoundTripper
	username, password string
}

func (b basicAuth) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.SetBasicAuth(b.username, b.password)
	return b.next.RoundTrip(r)
}
