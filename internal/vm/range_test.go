package vm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestQueryAtAndRange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/api/v1/query" && q.Get("time") == "1793613600.000":
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
				{"metric":{"__name__":"probe_success","location":"edge-1"},"value":[1793613600,"0"]}]}}`))
		case r.URL.Path == "/api/v1/query_range" && q.Get("start") == "1793611800.000" && q.Get("end") == "1793613600.000" && q.Get("step") == "60":
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
				{"metric":{"location":"edge-1"},"values":[[1793611800,"1"],[1793611860,"0"],[1793611920,"x"]]}]}}`))
		case r.URL.Path == "/api/v1/query_range":
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
		default:
			http.Error(w, "bad", http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, nil)
	at := time.Unix(1793613600, 0)
	got, err := c.QueryAt(context.Background(), "probe_success", at)
	if err != nil || len(got) != 1 || got[0].Labels["__name__"] != "probe_success" || got[0].Value != 0 {
		t.Fatalf("QueryAt: %+v %v", got, err)
	}
	rs, err := c.QueryRange(context.Background(), "probe_success", at.Add(-30*time.Minute), at, time.Minute)
	if err != nil || len(rs) != 1 || len(rs[0].Points) != 2 || rs[0].Points[1].V != 0 || !rs[0].Points[1].T.Equal(time.Unix(1793611860, 0)) {
		t.Fatalf("QueryRange: %+v %v", rs, err)
	}
	if _, err := c.QueryRange(context.Background(), "probe_success", at.Add(-time.Hour), at, time.Minute); err == nil {
		t.Error("a vector where a matrix belongs is an error")
	}
	if _, err := c.QueryAt(context.Background(), "up", time.Time{}); err == nil {
		t.Error("a 400 is an error")
	}
	var nilc *Client
	if s, err := nilc.QueryRange(context.Background(), "up", at, at, time.Minute); s != nil || err != nil {
		t.Error("a nil client queries nothing")
	}
}
