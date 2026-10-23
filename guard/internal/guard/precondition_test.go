package guard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPrometheusPreconditions(t *testing.T) {
	var lastQuery string
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastQuery = r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		if lastQuery == `mem{ns="shop",name="web"} > 300` {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"412"]}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer prom.Close()
	p := &PrometheusProber{URL: prom.URL}
	spec := PreconditionSpec{Name: "memory growth", Query: `mem{ns="{{.Namespace}}",name="{{.Name}}"} > 300`}

	got := p.Check(context.Background(), spec, Target{Namespace: "shop", Name: "web"})
	if got.Status != CheckObserved || got.Value != "412" {
		t.Fatalf("%+v", got)
	}
	if got := p.Check(context.Background(), spec, Target{Namespace: "shop", Name: "api"}); got.Status != CheckNotObserved {
		t.Fatalf("%+v", got)
	}
	lastQuery = ""
	evil := Target{Namespace: "shop", Name: `web"} or vector(1) #`}
	if got := p.Check(context.Background(), spec, evil); got.Status != CheckUnknown || lastQuery != "" {
		t.Fatalf("a target name rewrote the query: %+v (sent %q)", got, lastQuery)
	}
	if got := (&PrometheusProber{}).Check(context.Background(), spec, Target{Name: "web"}); got.Status != CheckUnknown {
		t.Fatal("no Prometheus must mean unknown, never observed")
	}
	down := &PrometheusProber{URL: "http://127.0.0.1:1"}
	if got := down.Check(context.Background(), spec, Target{Name: "web"}); got.Status != CheckUnknown {
		t.Fatal("an unreachable Prometheus must mean unknown")
	}
}
