package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/guard"
	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/store"
)

func readReview(w http.ResponseWriter, r *http.Request) (*admissionv1.AdmissionReview, bool) {
	var review admissionv1.AdmissionReview
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&review); err != nil || review.Request == nil {
		writeError(w, 400, "expected an AdmissionReview")
		return nil, false
	}
	return &review, true
}

func respond(w http.ResponseWriter, review *admissionv1.AdmissionReview, resp *admissionv1.AdmissionResponse) bool {
	resp.UID = review.Request.UID
	out := admissionv1.AdmissionReview{TypeMeta: review.TypeMeta, Response: resp}
	if out.APIVersion == "" {
		out.APIVersion, out.Kind = "admission.k8s.io/v1", "AdmissionReview"
	}
	writeJSON(w, 200, out)
	return resp.Allowed
}

func deny(msg string) *admissionv1.AdmissionResponse {
	return &admissionv1.AdmissionResponse{Allowed: false,
		Result: &metav1.Status{Status: metav1.StatusFailure, Code: 403, Reason: metav1.StatusReasonForbidden, Message: msg}}
}

// admitWorkload is the enforcement point: a change by an agent identity is admitted only when
// policy allows it on its own or a human approved exactly this change (and then only once).
// Anything unexpected denies: for agents the guard fails closed.
func (s *Server) admitWorkload(w http.ResponseWriter, r *http.Request) bool {
	review, ok := readReview(w, r)
	if !ok {
		return false
	}
	req := review.Request
	if !s.Engine.Policy().IsAgent(req.UserInfo.Username, req.UserInfo.Groups) {
		// The webhook's matchConditions already send only agents here; this is the second check.
		return respond(w, review, &admissionv1.AdmissionResponse{Allowed: true})
	}
	p, err := guard.ClassifyAdmission(req)
	if err != nil {
		return respond(w, review, deny("Sentinel Guard could not read this change: "+err.Error()))
	}
	if p == nil {
		return respond(w, review, &admissionv1.AdmissionResponse{Allowed: true})
	}
	if p.Generation == 0 && req.SubResource == "scale" && s.Generation != nil {
		// A Scale object carries no generation: read the workload's, only if the engine needs it.
		gvr, ns, name := schema.GroupVersionResource{Group: req.Resource.Group, Version: req.Resource.Version, Resource: req.Resource.Resource}, req.Namespace, req.Name
		p.LookupGeneration = func(ctx context.Context) (int64, error) { return s.Generation(ctx, gvr, ns, name) }
	}
	out, err := s.Engine.Evaluate(r.Context(), *p)
	if err != nil {
		s.Log.Error("admission evaluation failed", "error", err, "agent", p.Agent, "action", p.Action, "target", p.Target.String())
		return respond(w, review, deny("Sentinel Guard could not record this request, so it is denied (fail closed): "+err.Error()))
	}
	ann := map[string]string{"verdict": string(out.Verdict), "action": p.Action}
	if out.Request != nil {
		ann["request"] = out.Request.ID
	}
	if p.DryRun { // server-side dry runs are previews: always admitted, with what would happen
		return respond(w, review, &admissionv1.AdmissionResponse{Allowed: true, Warnings: []string{out.Message}, AuditAnnotations: ann})
	}
	if out.Verdict == guard.VerdictAllow {
		return respond(w, review, &admissionv1.AdmissionResponse{Allowed: true, Warnings: []string{out.Message}, AuditAnnotations: ann})
	}
	resp := deny(out.Message)
	resp.AuditAnnotations = ann
	return respond(w, review, resp)
}

// admitApproval protects the ActionRequest records. Only the guard creates, deletes or changes
// them; a human may set spec.decision once, and this webhook stamps who did it and when, so the
// approver's identity comes from the API server, never from what the client wrote.
func (s *Server) admitApproval(w http.ResponseWriter, r *http.Request) bool {
	review, ok := readReview(w, r)
	if !ok {
		return false
	}
	req := review.Request
	user := req.UserInfo.Username
	if user == s.GuardUser {
		return respond(w, review, &admissionv1.AdmissionResponse{Allowed: true})
	}
	switch req.Operation {
	case admissionv1.Create:
		return respond(w, review, deny("ActionRequests are created by Sentinel Guard only"))
	case admissionv1.Delete:
		return respond(w, review, deny("ActionRequests are the audit record: they cannot be deleted"))
	case admissionv1.Update:
	default:
		return respond(w, review, &admissionv1.AdmissionResponse{Allowed: true})
	}
	oldObj, newObj := &unstructured.Unstructured{}, &unstructured.Unstructured{}
	if err := json.Unmarshal(req.OldObject.Raw, &oldObj.Object); err != nil {
		return respond(w, review, deny("unreadable ActionRequest"))
	}
	if err := json.Unmarshal(req.Object.Raw, &newObj.Object); err != nil {
		return respond(w, review, deny("unreadable ActionRequest"))
	}
	before, err1 := store.FromObject(oldObj)
	after, err2 := store.FromObject(newObj)
	if err1 != nil || err2 != nil {
		return respond(w, review, deny("unreadable ActionRequest"))
	}
	if !reflect.DeepEqual(before.Approvals, after.Approvals) {
		return respond(w, review, deny("spec.approvals is written by Sentinel Guard: approve with spec.decision (kubectl sentinel approve)"))
	}
	if !sameRequest(before, after) {
		return respond(w, review, deny("what was requested cannot be edited; only spec.decision (approve or deny) and spec.decisionNote"))
	}
	if before.Decision == after.Decision && before.DecisionNote == after.DecisionNote && before.DecidedBy == after.DecidedBy &&
		before.DecidedVia == after.DecidedVia && reflect.DeepEqual(before.DecidedAt, after.DecidedAt) {
		return respond(w, review, &admissionv1.AdmissionResponse{Allowed: true}) // labels, annotations...
	}
	if after.Decision != "approve" && after.Decision != "deny" {
		return respond(w, review, deny(`spec.decision must be "approve" or "deny"`))
	}
	isAgent := s.Engine.Policy().IsAgent(user, req.UserInfo.Groups)
	if err := guard.CanDecide(before, user, isAgent, s.Engine.Now()); err != nil {
		return respond(w, review, deny("Sentinel Guard: "+err.Error()))
	}
	now := s.Engine.Now().UTC()
	if after.Decision == "approve" {
		if err := guard.CanApprove(before, user); err != nil {
			return respond(w, review, deny("Sentinel Guard: "+err.Error()))
		}
		if guard.Partial(before) {
			// One of several approvals: record it (as the authenticated user) and keep the request
			// pending. The guard signs it as an approval-added event.
			approvals := append(append([]guard.Approval(nil), before.Approvals...),
				guard.Approval{By: user, At: now, Note: after.DecisionNote})
			ops := []map[string]any{
				{"op": "add", "path": "/spec/approvals", "value": approvals},
				{"op": "remove", "path": "/spec/decision"},
			}
			if spec, _ := newObj.Object["spec"].(map[string]any); spec != nil {
				if _, ok := spec["decisionNote"]; ok {
					ops = append(ops, map[string]any{"op": "remove", "path": "/spec/decisionNote"})
				}
			}
			patch, _ := json.Marshal(ops)
			pt := admissionv1.PatchTypeJSONPatch
			return respond(w, review, &admissionv1.AdmissionResponse{Allowed: true, Patch: patch, PatchType: &pt,
				Warnings: []string{fmt.Sprintf("Sentinel Guard: approval %d of %d recorded for %s; it needs %d more, from other people",
					len(approvals), before.Needed(), before.ID, before.Needed()-len(approvals))},
				AuditAnnotations: map[string]string{"decision": "approval", "request": before.ID}})
		}
	}
	patch, _ := json.Marshal([]map[string]any{
		{"op": "add", "path": "/spec/decidedBy", "value": user},
		{"op": "add", "path": "/spec/decidedVia", "value": ""}, // a person deciding through the API server attests nothing for others
		{"op": "add", "path": "/spec/decidedAt", "value": now.Format(time.RFC3339Nano)},
	})
	pt := admissionv1.PatchTypeJSONPatch
	return respond(w, review, &admissionv1.AdmissionResponse{Allowed: true, Patch: patch, PatchType: &pt,
		AuditAnnotations: map[string]string{"decision": after.Decision, "request": before.ID}})
}

// sameRequest compares everything except the decision fields.
func sameRequest(a, b *guard.Request) bool {
	strip := func(r *guard.Request) string {
		c := *r
		c.Decision, c.DecidedBy, c.DecidedVia, c.DecisionNote, c.DecidedAt, c.Approvals = "", "", "", "", nil, nil
		// status is written by the guard through the status subresource, never through this path
		c.Phase, c.Reasons, c.Preconditions, c.ExecutedAt, c.CapabilityJTI, c.Audit, c.Exact, c.ExecGeneration = "", nil, nil, nil, "", nil, "", 0
		c.ExpiresAt = time.Time{}
		return fmt.Sprintf("%+v", c)
	}
	return strip(a) == strip(b)
}
