package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/guard"
)

// Notifier tells people when a request needs them, by POSTing JSON to a webhook: Slack's and
// Discord's incoming webhooks read the text, anything else can read the structured fields. Sending
// happens off the request path, from a bounded queue; a full queue drops (and counts) a notice
// rather than slow an agent or a person down.
type Notifier struct {
	URL       string
	PublicURL string // the guard's address for people (links to the inbox); empty in Kubernetes
	Client    *http.Client
	Log       *slog.Logger
	Sent      *prometheus.CounterVec // sentinel_guard_notifications_total{result}
	queue     chan Notice
}

// Notice is what the webhook receives.
type Notice struct {
	Text    string       `json:"text"`    // Slack
	Content string       `json:"content"` // Discord
	Event   NoticeDetail `json:"event"`
}

type NoticeDetail struct {
	Kind      string            `json:"kind"` // approval_needed | expired_undecided
	RequestID string            `json:"requestId"`
	Agent     string            `json:"agent"`
	Action    string            `json:"action"`
	Target    string            `json:"target"`
	Change    map[string]string `json:"change,omitempty"`
	Intent    string            `json:"intent,omitempty"`
	Risk      string            `json:"risk"`
	Needed    int               `json:"approvalsNeeded"` // still needed, from other people
	Approvals []string          `json:"approvedBy,omitempty"`
	ExpiresAt time.Time         `json:"expiresAt"`
	URL       string            `json:"url,omitempty"`
}

func NewNotifier(url, publicURL string, log *slog.Logger, reg prometheus.Registerer) *Notifier {
	n := &Notifier{URL: url, PublicURL: strings.TrimSuffix(publicURL, "/"), Client: &http.Client{Timeout: 5 * time.Second},
		Log: log, queue: make(chan Notice, 256),
		Sent: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "sentinel_guard_notifications_total",
			Help: "Notices sent to the notification webhook (SENTINEL_GUARD_NOTIFY_URL), by result: sent, failed, dropped."},
			[]string{"result"})}
	if reg != nil {
		reg.MustRegister(n.Sent)
	}
	return n
}

// Run sends queued notices until ctx ends.
func (n *Notifier) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case notice := <-n.queue:
			n.send(ctx, notice)
		}
	}
}

// Observe turns an audit event into a notice when it means a person is needed, or a request
// expired with nobody deciding.
func (n *Notifier) Observe(e guard.Event, r *guard.Request) {
	var kind string
	switch {
	case e.Type == guard.EventRequested && r.Phase == guard.PhasePending,
		e.Type == guard.EventApprovalAdded && r.Phase == guard.PhasePending:
		kind = "approval_needed"
	case e.Type == guard.EventExpired && r.Decision == "":
		kind = "expired_undecided"
	default:
		return
	}
	notice := n.notice(kind, r)
	select {
	case n.queue <- notice:
	default:
		n.Sent.WithLabelValues("dropped").Inc()
	}
}

func (n *Notifier) notice(kind string, r *guard.Request) Notice {
	d := NoticeDetail{Kind: kind, RequestID: r.ID, Agent: r.Agent, Action: r.Action, Target: r.Target.String(),
		Change: r.Change, Intent: r.Intent, Risk: r.Risk, Needed: r.Needed() - len(r.Approvals), ExpiresAt: r.ExpiresAt}
	for _, a := range r.Approvals {
		d.Approvals = append(d.Approvals, a.By)
	}
	if n.PublicURL != "" {
		d.URL = n.PublicURL + "/inbox/#" + r.ID // opens the request's details
	}
	keys := make([]string, 0, len(r.Change))
	for k := range r.Change {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var change []string
	for _, k := range keys {
		change = append(change, k+": "+r.Change[k])
	}
	var b strings.Builder
	switch kind {
	case "approval_needed":
		fmt.Fprintf(&b, "Sentinel Guard: %s asks to %s %s", r.Agent, r.Action, d.Target)
		if len(change) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(change, ", "))
		}
		fmt.Fprintf(&b, ", %s risk.", r.Risk)
		if r.Intent != "" {
			fmt.Fprintf(&b, " Why: %q.", r.Intent)
		}
		if len(d.Approvals) > 0 {
			fmt.Fprintf(&b, " Approved by %s; needs %d more, from someone else.", strings.Join(d.Approvals, ", "), d.Needed)
		} else if d.Needed > 1 {
			fmt.Fprintf(&b, " Needs %d different people to approve.", d.Needed)
		}
	case "expired_undecided":
		fmt.Fprintf(&b, "Sentinel Guard: request %s (%s asked to %s %s) expired with nobody deciding.", r.ID, r.Agent, r.Action, d.Target)
	}
	switch {
	case kind != "approval_needed":
	case d.URL != "":
		fmt.Fprintf(&b, " Decide: %s", d.URL)
	default:
		fmt.Fprintf(&b, " Decide: kubectl sentinel approve %s (or deny)", r.ID)
	}
	return Notice{Text: b.String(), Content: b.String(), Event: d}
}

// send POSTs a notice, retrying twice on a network error or a 5xx answer.
func (n *Notifier) send(ctx context.Context, notice Notice) {
	body, _ := json.Marshal(notice)
	var last string
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, bytes.NewReader(body))
		if err != nil {
			last = err.Error()
			break
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := n.Client.Do(req)
		if err != nil {
			last = err.Error()
			continue
		}
		res.Body.Close()
		if res.StatusCode < 300 {
			n.Sent.WithLabelValues("sent").Inc()
			return
		}
		last = fmt.Sprintf("HTTP %d", res.StatusCode)
		if res.StatusCode < 500 {
			break
		}
	}
	n.Sent.WithLabelValues("failed").Inc()
	if n.Log != nil {
		n.Log.Warn("notification not delivered", "request", notice.Event.RequestID, "error", last)
	}
}
