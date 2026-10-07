package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReviewWorkflowOverHTTP(t *testing.T) {
	h := newServer(t, 100)
	cid, _, lid := lectureFlow(t, h)
	course := "/v1/courses/" + cid

	resp, body := call(h, "GET", "/v1/settings/course-publishing", instr, instrRL, "")
	if must(t, resp, 200, body)["publishing_policy"] != "review_required" {
		t.Fatalf("instructor settings = %v", body)
	}
	resp, body = call(h, "GET", "/v1/settings/course-publishing", admin, adminRL, "")
	if must(t, resp, 200, body)["publishing_policy"] != "independent" {
		t.Fatalf("admin settings = %v", body)
	}

	resp, body = call(h, "POST", course+"/submit", instr, instrRL, "")
	must(t, resp, 204, body)
	resp, body = call(h, "GET", course, instr, instrRL, "")
	if must(t, resp, 200, body)["status"] != "in_review" || body["submitted_at"] == nil {
		t.Fatalf("submitted course = %v", body)
	}
	resp, body = call(h, "PATCH", course+"/lectures/"+lid, instr, instrRL, `{"title":"Edit"}`)
	expectProblem(t, resp, body, 409, "course_not_editable")

	resp, body = call(h, "GET", "/v1/courses/in-review", instr, instrRL, "")
	expectProblem(t, resp, body, 403, "forbidden")
	resp, body = call(h, "GET", "/v1/courses/in-review", admin, adminRL, "")
	if must(t, resp, 200, body)["total"] != float64(1) {
		t.Fatalf("queue = %v", body)
	}

	resp, body = call(h, "POST", course+"/approve", instr, instrRL, "")
	expectProblem(t, resp, body, 403, "forbidden")
	resp, body = call(h, "POST", course+"/request-changes", admin, adminRL, `{"note":" "}`)
	expectProblem(t, resp, body, 400, "review_note_required")
	resp, body = call(h, "POST", course+"/request-changes", admin, adminRL, `{"note":"Add a summary"}`)
	must(t, resp, 204, body)
	resp, body = call(h, "GET", course, instr, instrRL, "")
	if must(t, resp, 200, body)["status"] != "changes_requested" || body["latest_review_note"] != "Add a summary" {
		t.Fatalf("sent back course = %v", body)
	}

	resp, body = call(h, "POST", course+"/submit", instr, instrRL, "")
	must(t, resp, 204, body)
	resp, body = call(h, "POST", course+"/approve", admin, adminRL, `{"note":"Looks good"}`)
	must(t, resp, 204, body)
	resp, body = call(h, "POST", course+"/publish", instr, instrRL, "")
	must(t, resp, 204, body)

	resp, body = call(h, "POST", course+"/unpublish", admin, adminRL, `{}`)
	expectProblem(t, resp, body, 400, "review_note_required")
	resp, body = call(h, "POST", course+"/unpublish", admin, adminRL, `{"note":"Outdated"}`)
	must(t, resp, 204, body)
	resp, body = call(h, "POST", course+"/unpublish", admin, adminRL, `{"note":"Again"}`)
	expectProblem(t, resp, body, 409, "invalid_transition")
	resp, body = call(h, "GET", course, "", "", "")
	expectProblem(t, resp, body, 404, "not_found")

	reviews := listReviews(t, h, course, instr, instrRL)
	want := []struct{ reviewer, decision, note string }{
		{instr, "submitted", ""},
		{admin, "changes_requested", "Add a summary"},
		{instr, "submitted", ""},
		{admin, "approved", "Looks good"},
		{admin, "unpublished", "Outdated"},
	}
	if len(reviews) != len(want) {
		t.Fatalf("reviews = %v", reviews)
	}
	for i, w := range want {
		r := reviews[i]
		if r["reviewer_id"] != w.reviewer || r["decision"] != w.decision || r["note"] != w.note || r["id"] == "" || r["created_at"] == "" {
			t.Fatalf("reviews[%d] = %v, want %+v", i, r, w)
		}
	}
	resp, body = call(h, "GET", course+"/reviews", other, instrRL, "")
	expectProblem(t, resp, body, 404, "not_found")
}

func listReviews(t *testing.T, h http.Handler, course, user, role string) []map[string]any {
	t.Helper()
	r := httptest.NewRequest("GET", course+"/reviews", nil)
	r.Header.Set("X-Test-User", user)
	r.Header.Set("X-Test-Role", role)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("reviews = %d %s", w.Code, w.Body)
	}
	var out []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestLiveVersionOverHTTP(t *testing.T) {
	h := newServer(t, 100)
	cid, _, lid := lectureFlow(t, h)
	course, content := "/v1/courses/"+cid, "/v1/courses/"+cid+"/lectures/"+lid+"/content"
	resp, body := call(h, "POST", course+"/lectures/"+lid+"/free-preview", instr, instrRL, `{"free_preview":true}`)
	must(t, resp, 204, body)

	resp, body = call(h, "GET", course+"?view=live", instr, instrRL, "")
	expectProblem(t, resp, body, 404, "not_found")
	resp, body = call(h, "GET", course+"?view=bogus", instr, instrRL, "")
	expectProblem(t, resp, body, 400, "invalid_input")

	// Root admins publish without review.
	resp, body = call(h, "POST", course+"/publish", admin, adminRL, "")
	must(t, resp, 204, body)
	resp, body = call(h, "GET", course, instr, instrRL, "")
	if must(t, resp, 200, body)["status"] != "published" || body["live_version_number"] != float64(1) ||
		body["live_published_at"] == nil || body["has_draft_changes"] != false {
		t.Fatalf("published = %v", body)
	}

	resp, body = call(h, "PATCH", course, instr, instrRL, `{"title":"Go 2","description":"d"}`)
	must(t, resp, 204, body)
	resp, body = call(h, "PUT", content, instr, instrRL, `{"text_body":"<p>draft</p>"}`)
	must(t, resp, 204, body)

	resp, body = call(h, "GET", course, instr, instrRL, "")
	if must(t, resp, 200, body)["status"] != "draft" || body["title"] != "Go 2" || body["has_draft_changes"] != true {
		t.Fatalf("working copy = %v", body)
	}
	for _, read := range []struct{ user, role, query string }{{"", "", ""}, {instr, instrRL, "?view=live"}} {
		resp, body = call(h, "GET", course+read.query, read.user, read.role, "")
		if must(t, resp, 200, body)["title"] != "Go" || body["status"] != "published" || body["live_version_number"] != float64(1) {
			t.Fatalf("live course for %q = %v", read.user, body)
		}
		resp, body = call(h, "GET", content+read.query, stud, studRL, "")
		if must(t, resp, 200, body)["text_body"] != "<p>hello</p>" {
			t.Fatalf("live content = %v", body)
		}
	}
	resp, body = call(h, "GET", "/v1/courses", "", "", "")
	if courses := must(t, resp, 200, body)["courses"].([]any); len(courses) != 1 || courses[0].(map[string]any)["title"] != "Go" {
		t.Fatalf("catalog = %v", body)
	}

	// The instructor's edit goes live only after review.
	resp, body = call(h, "POST", course+"/publish", instr, instrRL, "")
	expectProblem(t, resp, body, 409, "approval_required")
	publish(t, h, cid)
	resp, body = call(h, "GET", course, "", "", "")
	if must(t, resp, 200, body)["title"] != "Go 2" || body["live_version_number"] != float64(2) {
		t.Fatalf("republished = %v", body)
	}
	resp, body = call(h, "GET", content, stud, studRL, "")
	if must(t, resp, 200, body)["text_body"] != "<p>draft</p>" {
		t.Fatalf("republished content = %v", body)
	}
}
