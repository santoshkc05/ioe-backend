package httpapi_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestVersionsAndDiscardDraftOverHTTP(t *testing.T) {
	h := newServer(t, 100)
	cid, _, lid := lectureFlow(t, h)
	course, content := "/v1/courses/"+cid, "/v1/courses/"+cid+"/lectures/"+lid+"/content"

	resp, body := call(h, "POST", course+"/discard-draft", instr, instrRL, "")
	expectProblem(t, resp, body, 409, "invalid_transition")
	resp, body = call(h, "POST", course+"/publish", admin, adminRL, "")
	must(t, resp, 204, body)

	resp, body = call(h, "PATCH", course, instr, instrRL, `{"title":"Go 2","description":"d"}`)
	must(t, resp, 204, body)
	resp, body = call(h, "PUT", content, instr, instrRL, `{"text_body":"<p>draft</p>"}`)
	must(t, resp, 204, body)

	resp, body = call(h, "POST", course+"/discard-draft", other, instrRL, "")
	expectProblem(t, resp, body, 403, "forbidden")
	resp, body = call(h, "POST", course+"/discard-draft", instr, instrRL, "")
	if must(t, resp, 200, body)["title"] != "Go" || body["status"] != "published" || body["has_draft_changes"] != false {
		t.Fatalf("discarded = %v", body)
	}
	resp, body = call(h, "GET", content, instr, instrRL, "")
	if must(t, resp, 200, body)["text_body"] != "<p>hello</p>" {
		t.Fatalf("restored content = %v", body)
	}

	r := httptest.NewRequest("GET", course+"/versions", nil)
	r.Header.Set("X-Test-User", instr)
	r.Header.Set("X-Test-Role", instrRL)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var versions []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &versions); err != nil || w.Code != 200 {
		t.Fatalf("versions = %d %s", w.Code, w.Body)
	}
	if len(versions) != 1 || versions[0]["version_number"] != float64(1) || versions[0]["published_by"] != admin ||
		versions[0]["published_at"] == nil {
		t.Fatalf("versions = %v", versions)
	}

	resp, body = call(h, "GET", course+"/versions/1", instr, instrRL, "")
	if must(t, resp, 200, body)["title"] != "Go" || body["status"] != "published" {
		t.Fatalf("version 1 = %v", body)
	}
	for _, n := range []string{"2", "0", "x"} {
		resp, body = call(h, "GET", course+"/versions/"+n, instr, instrRL, "")
		expectProblem(t, resp, body, 404, "not_found")
	}
	resp, body = call(h, "GET", course+"/versions/1", stud, studRL, "")
	expectProblem(t, resp, body, 403, "forbidden")
}
