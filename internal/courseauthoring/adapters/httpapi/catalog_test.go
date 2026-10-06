package httpapi_test

import (
	"net/http"
	"testing"
)

// publishedCourse creates a course with one lecture and publishes it.
func publishedCourse(t *testing.T, h http.Handler) string {
	t.Helper()
	cid, _, _ := lectureFlow(t, h)
	resp, body := call(h, "POST", "/v1/courses/"+cid+"/publish", instr, instrRL, "")
	if resp.StatusCode != 204 {
		t.Fatalf("publish = %d %v", resp.StatusCode, body)
	}
	return cid
}

func TestCatalogAnonymousListShapeAndPaging(t *testing.T) {
	h := newServer(t, 100)
	older := publishedCourse(t, h)
	newer := publishedCourse(t, h)
	newCourse(t, h) // draft

	resp, body := call(h, "GET", "/v1/courses?limit=1", "", "", "")
	must(t, resp, 200, body)
	courses := body["courses"].([]any)
	if len(courses) != 1 || body["next_cursor"] != newer {
		t.Fatalf("page1 = %v", body)
	}
	c := courses[0].(map[string]any)
	price, _ := c["price"].(map[string]any)
	if c["id"] != newer || c["owner_id"] != instr || c["title"] != "Go" || c["is_free"] != true ||
		price["amount_minor"] != float64(0) || c["lecture_count"] != float64(1) || c["section_count"] != float64(1) ||
		c["level"] != "" || c["thumbnail_url"] != "" || c["created_at"] == nil || c["updated_at"] == nil {
		t.Fatalf("summary = %v", c)
	}
	for _, absent := range []string{"sections", "lectures", "status"} {
		if _, ok := c[absent]; ok {
			t.Errorf("summary has %q", absent)
		}
	}

	resp, body = call(h, "GET", "/v1/courses?limit=1&cursor="+newer, "", "", "")
	must(t, resp, 200, body)
	if courses := body["courses"].([]any); len(courses) != 1 || courses[0].(map[string]any)["id"] != older {
		t.Fatalf("page2 = %v", body)
	}
	if _, ok := body["next_cursor"]; ok {
		t.Fatalf("last page has next_cursor: %v", body)
	}
}

func TestCatalogEmptyIsArray(t *testing.T) {
	h := newServer(t, 100)
	resp, body := call(h, "GET", "/v1/courses?price=paid", "", "", "")
	must(t, resp, 200, body)
	if courses, ok := body["courses"].([]any); !ok || len(courses) != 0 {
		t.Fatalf("body = %v", body)
	}
}

func TestCatalogFiltersReachTheService(t *testing.T) {
	h := newServer(t, 100)
	cid := publishedCourse(t, h)
	resp, body := call(h, "GET", "/v1/courses?price=free&level=", "", "", "")
	expectProblem(t, resp, body, 400, "invalid_input")
	resp, body = call(h, "GET", "/v1/courses?price=free", "", "", "")
	if got := must(t, resp, 200, body)["courses"].([]any); len(got) != 1 || got[0].(map[string]any)["id"] != cid {
		t.Fatalf("free = %v", body)
	}
	resp, body = call(h, "GET", "/v1/courses?level=advanced", "", "", "")
	if got := must(t, resp, 200, body)["courses"].([]any); len(got) != 0 {
		t.Fatalf("advanced = %v", body)
	}
}

func TestCatalogInvalidParameters(t *testing.T) {
	h := newServer(t, 100)
	for _, qs := range []string{
		"limit=0", "limit=51", "limit=-1", "limit=abc", "limit=",
		"level=", "level=expert", "price=cheap", "price=",
		"cursor=abc", "cursor=0", "cursor=",
	} {
		resp, body := call(h, "GET", "/v1/courses?"+qs, "", "", "")
		if resp.StatusCode != 400 || body["type"] != "invalid_input" {
			t.Errorf("%s: %d %v", qs, resp.StatusCode, body)
		}
	}
	resp, body := call(h, "GET", "/v1/courses?sort=title&limit=50", "", "", "")
	must(t, resp, 200, body) // unknown parameters are ignored
}

func TestCatalogInvalidTokenIsUnauthorized(t *testing.T) {
	h := newServer(t, 100)
	cid := publishedCourse(t, h)
	for _, p := range []string{"/v1/courses", "/v1/courses/" + cid} {
		resp, body := call(h, "GET", p, "bad", "", "")
		expectProblem(t, resp, body, 401, "invalid_token")
	}
}

func TestAnonymousCourseDetail(t *testing.T) {
	h := newServer(t, 100)
	published := publishedCourse(t, h)
	draft := newCourse(t, h)

	resp, body := call(h, "GET", "/v1/courses/"+published, "", "", "")
	if got := must(t, resp, 200, body); got["id"] != published || len(got["lectures"].([]any)) != 1 {
		t.Fatalf("published = %v", got)
	}
	resp, body = call(h, "GET", "/v1/courses/"+draft, "", "", "")
	expectProblem(t, resp, body, 404, "not_found")
	resp, body = call(h, "GET", "/v1/courses/"+draft, instr, instrRL, "")
	must(t, resp, 200, body) // the owner still sees the draft
}

func TestPrivateRoutesStillRequireAuth(t *testing.T) {
	h := newServer(t, 100)
	cid, _, lid := lectureFlow(t, h)
	for _, req := range []struct{ method, path, body string }{
		{"POST", "/v1/courses", `{"title":"Go","description":""}`},
		{"GET", "/v1/users/" + instr + "/courses", ""},
		{"GET", "/v1/courses/" + cid + "/lectures/" + lid + "/content", ""},
		{"PATCH", "/v1/courses/" + cid, `{"title":"X","description":"","level":"","thumbnail_url":""}`},
	} {
		resp, body := call(h, req.method, req.path, "", "", req.body)
		if resp.StatusCode != 401 {
			t.Errorf("%s %s: %d %v", req.method, req.path, resp.StatusCode, body)
		}
	}
}

func TestCatalogRateLimited(t *testing.T) {
	h := newServer(t, 2)
	var last response
	for range 3 {
		last, _ = call(h, "GET", "/v1/courses", "", "", "")
	}
	if last.StatusCode != 429 || last.Header.Get("Retry-After") == "" {
		t.Fatalf("third call = %d", last.StatusCode)
	}
}
