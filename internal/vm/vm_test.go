package vm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" || r.URL.Query().Get("query") != `probe_success{service="git"}` {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
			{"metric":{"location":"us-los-angeles"},"value":[1793080000,"1"]},
			{"metric":{"location":"cn-shanghai-bgp"},"value":[1793080000,"0"]},
			{"metric":{"location":"x"},"value":[1793080000,"NaNx"]}]}}`))
	}))
	defer srv.Close()
	got, err := New(srv.URL, nil).Query(context.Background(), `probe_success{service="git"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Labels["location"] != "us-los-angeles" || got[0].Value != 1 || got[1].Value != 0 {
		t.Fatalf("%+v", got)
	}
	if _, err := New(srv.URL, nil).Query(context.Background(), "up"); err == nil {
		t.Error("a 400 should be an error")
	}
	var c *Client
	if s, err := c.Query(context.Background(), "up"); s != nil || err != nil {
		t.Error("a nil client queries nothing")
	}
}
