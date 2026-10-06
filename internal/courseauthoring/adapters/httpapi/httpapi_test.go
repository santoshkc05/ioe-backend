package httpapi_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/adapters/httpapi"
	"github.com/santoshkc2200/ioe-backend/internal/courseauthoring/app"
	"github.com/santoshkc2200/ioe-backend/internal/platform/auth"
	"github.com/santoshkc2200/ioe-backend/internal/platform/httpserver"
	"github.com/santoshkc2200/ioe-backend/internal/platform/id"
	"github.com/santoshkc2200/ioe-backend/internal/platform/problem"
)

func fakeAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, err := id.Parse(r.Header.Get("X-Test-User"))
		if err != nil {
			problem.Write(w, r, http.StatusUnauthorized, "invalid_token", "Invalid Token", "")
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: uid, Role: auth.Role(r.Header.Get("X-Test-Role"))})))
	})
}

// fakeOptionalAuth passes requests without X-Test-User through anonymously and
// authenticates the rest like fakeAuth.
func fakeOptionalAuth(next http.Handler) http.Handler {
	authed := fakeAuth(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, present := r.Header["X-Test-User"]; !present {
			next.ServeHTTP(w, r)
			return
		}
		authed.ServeHTTP(w, r)
	})
}

func newServer(t *testing.T, perMinute int) http.Handler {
	t.Helper()
	store := newMemStore()
	ids, err := id.NewGenerator(0)
	if err != nil {
		t.Fatal(err)
	}
	clk := fixedClock{time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	r, h := httpserver.NewRouter(httpserver.Options{Logger: logger, AllowedOrigins: []string{"https://app.test"}, ServiceName: "test"})
	httpapi.New(app.NewCourseService(store, ids, clk, assetCatalog{}), app.NewContentService(store, ids, enrolled{}, assetCatalog{}), httpapi.Config{
		RequireAuth: fakeAuth, OptionalAuth: fakeOptionalAuth, IPs: httpserver.NewIPResolver(nil),
		ContentLimiter: httpserver.NewRateLimiter(perMinute), CatalogLimiter: httpserver.NewRateLimiter(perMinute),
		Logger: logger,
	}).Register(r)
	return h
}

// response is the status and headers of a call, with the body already decoded and closed.
type response struct {
	StatusCode int
	Header     http.Header
}

func call(h http.Handler, method, path, user, role, body string) (response, map[string]any) {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if user != "" {
		r.Header.Set("X-Test-User", user)
		r.Header.Set("X-Test-Role", role)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	res := w.Result()
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return response{StatusCode: res.StatusCode, Header: res.Header}, out
}

const (
	instr   = "100"
	instrRL = "instructor"
	other   = "101"
	stud    = "200"
	studRL  = "student"
)

func must(t *testing.T, resp response, want int, body map[string]any) map[string]any {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("status = %d, want %d, body = %v", resp.StatusCode, want, body)
	}
	return body
}

func expectProblem(t *testing.T, resp response, body map[string]any, status int, typ string) {
	t.Helper()
	if resp.StatusCode != status || body["type"] != typ {
		t.Fatalf("got %d %v, want %d %q", resp.StatusCode, body, status, typ)
	}
}

func newCourse(t *testing.T, h http.Handler) string {
	t.Helper()
	resp, body := call(h, "POST", "/v1/courses", instr, instrRL, `{"title":"Go","description":"d"}`)
	return must(t, resp, 201, body)["id"].(string)
}

// lectureFlow creates a course with one section and one text lecture, returning the three IDs.
func lectureFlow(t *testing.T, h http.Handler) (courseID, sectionID, lectureID string) {
	t.Helper()
	courseID = newCourse(t, h)
	resp, body := call(h, "POST", "/v1/courses/"+courseID+"/sections", instr, instrRL, `{"title":"Intro"}`)
	sectionID = must(t, resp, 201, body)["sections"].([]any)[0].(map[string]any)["id"].(string)
	resp, body = call(h, "POST", "/v1/courses/"+courseID+"/lectures", instr, instrRL, `{"title":"L1","text_body":"<p>hello</p>"}`)
	lecture := must(t, resp, 201, body)["lectures"].([]any)[0].(map[string]any)
	if lecture["has_text"] != true {
		t.Fatalf("lecture = %v", lecture)
	}
	return courseID, sectionID, lecture["id"].(string)
}

func TestCreateAndGetCourseWireShape(t *testing.T) {
	h := newServer(t, 100)
	resp, body := call(h, "POST", "/v1/courses", instr, instrRL, `{"title":"Go","description":"d"}`)
	must(t, resp, 201, body)
	cid, _ := body["id"].(string)
	price, _ := body["price"].(map[string]any)
	if cid == "" || body["owner_id"] != "100" || body["status"] != "draft" || body["is_free"] != true ||
		price["amount_minor"] != float64(0) || price["currency"] != "" ||
		len(body["sections"].([]any)) != 0 || len(body["lectures"].([]any)) != 0 ||
		body["thumbnail_url"] != "" || body["level"] != "" || body["created_at"] == nil {
		t.Fatalf("body = %v", body)
	}
	resp, got := call(h, "GET", "/v1/courses/"+cid+"?view=draft", instr, instrRL, "")
	if must(t, resp, 200, got)["id"] != cid {
		t.Fatalf("get = %v", got)
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	h := newServer(t, 100)
	resp, body := call(h, "POST", "/v1/courses", instr, instrRL, `{"title":"Go","description":"","position":1}`)
	expectProblem(t, resp, body, 400, "invalid_request")
}

func TestPathIDParseFailureIs404(t *testing.T) {
	h := newServer(t, 100)
	for _, p := range []string{"/v1/courses/abc", "/v1/courses/0"} {
		resp, body := call(h, "GET", p, instr, instrRL, "")
		expectProblem(t, resp, body, 404, "not_found")
	}
}

func TestDraftHiddenFromOthers(t *testing.T) {
	h := newServer(t, 100)
	cid := newCourse(t, h)
	resp, body := call(h, "GET", "/v1/courses/"+cid, stud, studRL, "")
	expectProblem(t, resp, body, 404, "not_found")
}

func TestLectureFlowAndContent(t *testing.T) {
	h := newServer(t, 100)
	cid, sid, lid := lectureFlow(t, h)
	base := "/v1/courses/" + cid + "/lectures/" + lid
	if resp, body := call(h, "POST", base+"/section", instr, instrRL, `{"section_id":"`+sid+`"}`); resp.StatusCode != 204 {
		t.Fatalf("move = %d %v", resp.StatusCode, body)
	}
	if resp, body := call(h, "POST", base+"/free-preview", instr, instrRL, `{"free_preview":true}`); resp.StatusCode != 204 {
		t.Fatalf("free preview = %d %v", resp.StatusCode, body)
	}
	resp, body := call(h, "GET", base+"/content", instr, instrRL, "")
	must(t, resp, 200, body)
	blocks := body["blocks"].([]any)
	if body["content_revision"] != float64(1) || len(blocks) != 1 || blocks[0].(map[string]any)["type"] != "text" || body["text_body"] == "" || body["free_preview"] != true {
		t.Fatalf("content = %v", body)
	}
	resp, course := call(h, "GET", "/v1/courses/"+cid, instr, instrRL, "")
	if must(t, resp, 200, course)["lectures"].([]any)[0].(map[string]any)["section_id"] != sid {
		t.Fatalf("course = %v", course)
	}
}

func TestPatchConflictBody(t *testing.T) {
	h := newServer(t, 100)
	cid, _, lid := lectureFlow(t, h)
	resp, body := call(h, "PATCH", "/v1/courses/"+cid+"/lectures/"+lid+"/content", instr, instrRL, `{"base_revision":0,"order":[]}`)
	expectProblem(t, resp, body, 409, "revision_conflict")
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type = %q", ct)
	}
	if body["lecture_id"] != lid || body["content_revision"] != float64(1) || len(body["blocks"].([]any)) != 1 {
		t.Fatalf("conflict body = %v", body)
	}
}

func TestPatchSuccess(t *testing.T) {
	h := newServer(t, 100)
	cid, _, lid := lectureFlow(t, h)
	path := "/v1/courses/" + cid + "/lectures/" + lid + "/content"
	_, content := call(h, "GET", path, instr, instrRL, "")
	existing := content["blocks"].([]any)[0].(map[string]any)["client_block_id"].(string)
	req := `{"base_revision":1,"order":["` + existing + `","n1"],"upserts":[{"client_block_id":"n1","type":"text","body":"<p>two</p>"}]}`
	resp, body := call(h, "PATCH", path, instr, instrRL, req)
	must(t, resp, 200, body)
	if body["content_revision"] != float64(2) {
		t.Fatalf("body = %v", body)
	}
	// A block carrying a position field is rejected as an unknown field.
	resp, body = call(h, "PATCH", path, instr, instrRL, `{"base_revision":2,"order":["n1"],"upserts":[{"client_block_id":"n1","type":"text","body":"x","position":0}]}`)
	expectProblem(t, resp, body, 400, "invalid_request")
}

func TestContentRateLimit(t *testing.T) {
	h := newServer(t, 2)
	cid, _, lid := lectureFlow(t, h)
	path := "/v1/courses/" + cid + "/lectures/" + lid + "/content"
	for i := 0; i < 2; i++ {
		if resp, _ := call(h, "PATCH", path, instr, instrRL, `{"base_revision":0,"order":[]}`); resp.StatusCode == 429 {
			t.Fatalf("call %d limited early", i)
		}
	}
	resp, body := call(h, "PATCH", path, instr, instrRL, `{"base_revision":0,"order":[]}`)
	expectProblem(t, resp, body, 429, "rate_limited")
	if resp.Header.Get("Retry-After") != "60" {
		t.Fatalf("Retry-After = %q", resp.Header.Get("Retry-After"))
	}
}

func TestErrorCodes(t *testing.T) {
	h := newServer(t, 100)

	empty := newCourse(t, h)
	resp, body := call(h, "POST", "/v1/courses/"+empty+"/publish", instr, instrRL, "")
	expectProblem(t, resp, body, 400, "empty_course")

	resp, body = call(h, "POST", "/v1/courses/"+empty+"/sections", instr, instrRL, `{"title":"S"}`)
	must(t, resp, 201, body)
	resp, body = call(h, "POST", "/v1/courses/"+empty+"/sections", instr, instrRL, `{"title":"S"}`)
	expectProblem(t, resp, body, 400, "duplicate_title")

	resp, body = call(h, "POST", "/v1/courses/"+empty+"/price", instr, instrRL, `{"amount_minor":100,"currency":"USD"}`)
	expectProblem(t, resp, body, 400, "unsupported_currency")

	resp, body = call(h, "POST", "/v1/courses/"+empty+"/lectures-order", instr, instrRL, `{"lecture_ids":[]}`)
	if resp.StatusCode != 405 {
		t.Fatalf("wrong method = %d %v", resp.StatusCode, body)
	}
	resp, body = call(h, "PUT", "/v1/courses/"+empty+"/lectures-order", instr, instrRL, `{"lecture_ids":["999"]}`)
	expectProblem(t, resp, body, 400, "invalid_input")

	cid, _, lid := lectureFlow(t, h)
	content := "/v1/courses/" + cid + "/lectures/" + lid + "/content"
	resp, body = call(h, "PATCH", content, instr, instrRL, `{"order":[]}`)
	expectProblem(t, resp, body, 400, "lecture_content_revision_required")

	resp, body = call(h, "PUT", content, instr, instrRL, `{"blocks":[{"client_block_id":"x","type":"text","body":"<p>hi</p><script>alert(1)</script>"}]}`)
	if resp.StatusCode == 204 {
		_, got := call(h, "GET", content, instr, instrRL, "")
		if strings.Contains(strings.ToLower(got["text_body"].(string)), "<script") {
			t.Fatalf("stored script: %v", got)
		}
	} else {
		expectProblem(t, resp, body, 400, "unsafe_content")
	}

	resp, body = call(h, "POST", "/v1/courses/"+cid+"/publish", instr, instrRL, "")
	if resp.StatusCode != 204 {
		t.Fatalf("publish = %d %v", resp.StatusCode, body)
	}
	resp, body = call(h, "GET", content, stud, studRL, "")
	expectProblem(t, resp, body, 403, "enrollment_required")

	resp, body = call(h, "POST", "/v1/courses/"+cid+"/archive", instr, instrRL, "")
	if resp.StatusCode != 204 {
		t.Fatalf("archive = %d %v", resp.StatusCode, body)
	}
	resp, body = call(h, "POST", "/v1/courses/"+cid+"/archive", instr, instrRL, "")
	expectProblem(t, resp, body, 409, "invalid_transition")
	resp, body = call(h, "PUT", content, instr, instrRL, `{"text_body":"<p>x</p>"}`)
	expectProblem(t, resp, body, 409, "course_not_editable")

	resp, body = call(h, "POST", "/v1/courses", stud, studRL, `{"title":"Nope"}`)
	expectProblem(t, resp, body, 403, "forbidden")
}

func TestListByOwner(t *testing.T) {
	h := newServer(t, 100)
	newCourse(t, h)
	resp, body := call(h, "GET", "/v1/users/100/courses", instr, instrRL, "")
	must(t, resp, 200, body)
	if body["total"] != float64(1) || len(body["courses"].([]any)) != 1 {
		t.Fatalf("body = %v", body)
	}
	resp, body = call(h, "GET", "/v1/users/100/courses", other, instrRL, "")
	expectProblem(t, resp, body, 403, "forbidden")
}

func TestInvalidMediaReference(t *testing.T) {
	h := newServer(t, 100)
	cid, _, lid := lectureFlow(t, h)
	resp, body := call(h, "PUT", "/v1/courses/"+cid+"/lectures/"+lid+"/content", instr, instrRL,
		`{"blocks":[{"client_block_id":"v","type":"video","media_asset_id":"9001","duration_ms":60000}]}`)
	expectProblem(t, resp, body, 400, "invalid_media_reference")
}
