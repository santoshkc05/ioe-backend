package httpapi_test

import (
	"net/http"
	"net/url"
	"testing"
)

func createCategory(t *testing.T, h http.Handler, name string) map[string]any {
	t.Helper()
	resp, body := call(h, "POST", "/v1/categories", admin, adminRL, `{"name":"`+name+`"}`)
	return must(t, resp, 201, body)
}

func TestCategoryRoutes(t *testing.T) {
	h := newServer(t, 100)
	resp, body := call(h, "POST", "/v1/categories", instr, instrRL, `{"name":"Web"}`)
	expectProblem(t, resp, body, 403, "forbidden")
	resp, body = call(h, "POST", "/v1/categories", "", "", `{"name":"Web"}`)
	if resp.StatusCode != 401 {
		t.Fatalf("anonymous create = %d %v", resp.StatusCode, body)
	}
	web := createCategory(t, h, "Web Development")
	if web["slug"] != "web-development" || web["name"] != "Web Development" || web["id"] == "" {
		t.Fatalf("created = %v", web)
	}
	resp, body = call(h, "POST", "/v1/categories", admin, adminRL, `{"name":"web development"}`)
	expectProblem(t, resp, body, 409, "category_exists")
	resp, body = call(h, "POST", "/v1/categories", admin, adminRL, `{"name":"  "}`)
	expectProblem(t, resp, body, 400, "invalid_category_name")

	resp, body = call(h, "PATCH", "/v1/categories/"+web["id"].(string), admin, adminRL, `{"name":"Web"}`)
	if got := must(t, resp, 200, body); got["slug"] != "web" {
		t.Fatalf("renamed = %v", got)
	}
	resp, body = call(h, "PATCH", "/v1/categories/999", admin, adminRL, `{"name":"X"}`)
	expectProblem(t, resp, body, 404, "not_found")

	resp, body = call(h, "GET", "/v1/categories", "", "", "")
	list := must(t, resp, 200, body)["categories"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["course_count"] != float64(0) {
		t.Fatalf("list = %v", body)
	}
	resp, body = call(h, "DELETE", "/v1/categories/"+web["id"].(string), admin, adminRL, "")
	must(t, resp, 204, body)
	resp, body = call(h, "DELETE", "/v1/categories/"+web["id"].(string), admin, adminRL, "")
	expectProblem(t, resp, body, 404, "not_found")
}

func TestUpdateDetailsClassificationOverHTTP(t *testing.T) {
	h := newServer(t, 100)
	web := createCategory(t, h, "Web")
	cid := newCourse(t, h)
	resp, body := call(h, "PATCH", "/v1/courses/"+cid, instr, instrRL,
		`{"title":"Go","category_ids":["`+web["id"].(string)+`"],"tags":["Go","web"]}`)
	must(t, resp, 204, body)
	resp, body = call(h, "GET", "/v1/courses/"+cid, instr, instrRL, "")
	got := must(t, resp, 200, body)
	cats := got["categories"].([]any)
	if len(cats) != 1 || cats[0].(map[string]any)["slug"] != "web" || len(got["tags"].([]any)) != 2 {
		t.Fatalf("course = %v", got)
	}
	for body, typ := range map[string]string{
		`{"title":"Go","category_ids":["999"]}`:                               "unknown_category",
		`{"title":"Go","category_ids":["1","2","3","4"]}`:                     "too_many_categories",
		`{"title":"Go","tags":["c++"]}`:                                       "invalid_tag",
		`{"title":"Go","tags":["a","b","c","d","e","f","g","h","i","j","k"]}`: "too_many_tags",
	} {
		resp, out := call(h, "PATCH", "/v1/courses/"+cid, instr, instrRL, body)
		expectProblem(t, resp, out, 400, typ)
	}
}

func TestUpdateDetailsTagsNullVersusEmpty(t *testing.T) {
	h := newServer(t, 100)
	cid := newCourse(t, h)
	tagsOf := func() []any {
		resp, body := call(h, "GET", "/v1/courses/"+cid, instr, instrRL, "")
		return must(t, resp, 200, body)["tags"].([]any)
	}
	resp, body := call(h, "PATCH", "/v1/courses/"+cid, instr, instrRL, `{"title":"Go","tags":["go"]}`)
	must(t, resp, 204, body)
	for _, keep := range []string{`{"title":"Go","tags":null}`, `{"title":"Go"}`} {
		resp, body = call(h, "PATCH", "/v1/courses/"+cid, instr, instrRL, keep)
		must(t, resp, 204, body)
		if got := tagsOf(); len(got) != 1 {
			t.Fatalf("%s cleared tags: %v", keep, got)
		}
	}
	resp, body = call(h, "PATCH", "/v1/courses/"+cid, instr, instrRL, `{"title":"Go","tags":[]}`)
	must(t, resp, 204, body)
	if got := tagsOf(); len(got) != 0 {
		t.Fatalf("[] kept tags: %v", got)
	}
}

func TestCatalogSearchAndFilterParameters(t *testing.T) {
	h := newServer(t, 100)
	web := createCategory(t, h, "Web")
	cid := publishedCourse(t, h)
	resp, body := call(h, "PATCH", "/v1/courses/"+cid, instr, instrRL,
		`{"title":"Go","category_ids":["`+web["id"].(string)+`"],"tags":["go"]}`)
	must(t, resp, 204, body)
	publish(t, h, cid)

	for _, qs := range []string{"q=go", "category=web", "tag=Go", "q=go&category=web&tag=go"} {
		resp, body := call(h, "GET", "/v1/courses?"+qs, "", "", "")
		courses := must(t, resp, 200, body)["courses"].([]any)
		if len(courses) != 1 {
			t.Fatalf("%s = %v", qs, body)
		}
		c := courses[0].(map[string]any)
		if c["categories"].([]any)[0].(map[string]any)["name"] != "Web" || c["tags"].([]any)[0] != "go" {
			t.Fatalf("%s summary = %v", qs, c)
		}
	}
	for _, qs := range []string{"q=", "q=%20%20", "category=", "tag="} {
		resp, body := call(h, "GET", "/v1/courses?"+qs, "", "", "")
		expectProblem(t, resp, body, 400, "invalid_input")
	}
	resp, body = call(h, "GET", "/v1/courses?tag=c%2B%2B", "", "", "")
	if got := must(t, resp, 200, body)["courses"].([]any); len(got) != 0 {
		t.Fatalf("invalid tag = %v", body)
	}
}

func TestCatalogSearchCursorBoundToQuery(t *testing.T) {
	h := newServer(t, 100)
	publishedCourse(t, h)
	publishedCourse(t, h)
	resp, body := call(h, "GET", "/v1/courses?q=go&limit=1", "", "", "")
	cursor, _ := must(t, resp, 200, body)["next_cursor"].(string)
	if cursor == "" {
		t.Fatalf("no cursor: %v", body)
	}
	resp, body = call(h, "GET", "/v1/courses?q=go&limit=1&cursor="+url.QueryEscape(cursor), "", "", "")
	if got := must(t, resp, 200, body)["courses"].([]any); len(got) != 1 {
		t.Fatalf("page 2 = %v", body)
	}
	for _, qs := range []string{"q=rust&cursor=" + url.QueryEscape(cursor), "cursor=" + url.QueryEscape(cursor), "q=go&cursor=123", "q=go&cursor=%21%21"} {
		resp, body := call(h, "GET", "/v1/courses?"+qs, "", "", "")
		expectProblem(t, resp, body, 400, "invalid_input")
	}
}
