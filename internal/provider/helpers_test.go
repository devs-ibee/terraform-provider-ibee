package provider

import (
	"encoding/json"
	"net/http"
	"testing"
)

// decodeBody decodes a JSON request body, keeping numbers exact.
func decodeBody(t *testing.T, req *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	dec := json.NewDecoder(req.Body)
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}
