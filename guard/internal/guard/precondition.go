package guard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"text/template"
	"time"
)

// Prober checks preconditions against telemetry the guard reads itself. This is what makes the
// guard independent: an agent's own evidence (which an attacker can shape through log lines) is
// never what allows an action to run on its own.
type Prober interface {
	Check(ctx context.Context, spec PreconditionSpec, t Target) Check
}

// PrometheusProber runs each precondition as an instant PromQL query: a non-empty result means
// the condition is observed.
type PrometheusProber struct {
	URL    string
	Client *http.Client
}

// Values substituted into queries must be plain identifiers, so a target name can't rewrite the
// query (e.g. a name like `x"} or vector(1) #`).
var safeLabel = regexp.MustCompile(`^[A-Za-z0-9._:/-]*$`)

func (p *PrometheusProber) Check(ctx context.Context, spec PreconditionSpec, t Target) Check {
	c := Check{Name: spec.Name, Status: CheckUnknown}
	if p == nil || p.URL == "" {
		c.Error = "no Prometheus configured"
		return c
	}
	query, err := renderQuery(spec.Query, t)
	if err != nil {
		c.Error = err.Error()
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(p.URL, "/")+"/api/v1/query?query="+url.QueryEscape(query), nil)
	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		c.Error = err.Error()
		return c
	}
	defer resp.Body.Close()
	var body struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			ResultType string            `json:"resultType"`
			Result     []json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Status != "success" {
		c.Error = fmt.Sprintf("prometheus: HTTP %d %s", resp.StatusCode, body.Error)
		return c
	}
	if len(body.Data.Result) == 0 {
		c.Status = CheckNotObserved
		return c
	}
	c.Status = CheckObserved
	var sample struct {
		Value []any `json:"value"`
	}
	if json.Unmarshal(body.Data.Result[0], &sample) == nil && len(sample.Value) == 2 {
		c.Value = fmt.Sprint(sample.Value[1])
	}
	return c
}

func renderQuery(q string, t Target) (string, error) {
	for _, v := range []string{t.Namespace, t.Name, t.Resource, t.Group} {
		if !safeLabel.MatchString(v) {
			return "", fmt.Errorf("target %q has characters not allowed in a query", v)
		}
	}
	tpl, err := template.New("q").Option("missingkey=error").Parse(q)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := tpl.Execute(&b, t); err != nil {
		return "", err
	}
	return b.String(), nil
}

func allObserved(checks []Check) bool {
	for _, c := range checks {
		if c.Status != CheckObserved {
			return false
		}
	}
	return len(checks) > 0
}
