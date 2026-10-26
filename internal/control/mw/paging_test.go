package mw

import (
	"net/http/httptest"
	"testing"
)

func TestParsePageDefaults(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/console/models?page=2&page_size=50&sort=model_name&order=asc", nil)
	p, apiErr := ParsePage(r, "model_name", "published_at")
	if apiErr != nil {
		t.Fatalf("ParsePage: %+v", apiErr)
	}
	if p.Number != 2 || p.Size != 50 || p.Sort != "model_name" || p.Order != "asc" {
		t.Fatalf("unexpected page: %+v", p)
	}
	if p.Offset() != 50 {
		t.Fatalf("offset = %d, want 50", p.Offset())
	}
}

func TestParsePageRejectsInvalid(t *testing.T) {
	cases := []string{
		"/x?page=0",
		"/x?page=-1",
		"/x?page=abc",
		"/x?page_size=0",
		"/x?page_size=101",
		"/x?sort=created_at", // not in the allowed list
		"/x?order=sideways",
	}
	for _, target := range cases {
		r := httptest.NewRequest("GET", target, nil)
		if _, apiErr := ParsePage(r, "model_name"); apiErr == nil {
			t.Fatalf("%s was accepted", target)
		} else if apiErr.HTTPStatus != 400 {
			t.Fatalf("%s status = %d, want 400", target, apiErr.HTTPStatus)
		}
	}
}

func TestParsePageMaxSize(t *testing.T) {
	r := httptest.NewRequest("GET", "/x?page_size=100", nil)
	if _, apiErr := ParsePage(r); apiErr != nil {
		t.Fatalf("page_size=100 rejected: %+v", apiErr)
	}
}
