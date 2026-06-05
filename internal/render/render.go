// Package render implements crmkit's content negotiation. Responses default to
// grepable plain text (one labeled line per record); JSON is returned only when
// the client asks for it via the Accept header or a ?format=json override.
package render

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// WantJSON reports whether the client prefers a JSON response. Plain text is
// the default; JSON is selected by an "application/json" Accept header or a
// "format=json" query parameter.
func WantJSON(r *http.Request) bool {
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("format")), "json") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("format")), "text") {
		return false
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/json")
}

// Respond writes either the JSON object or the plain-text body depending on
// what the client negotiated.
func Respond(w http.ResponseWriter, r *http.Request, status int, jsonObj any, text string) {
	if WantJSON(r) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(jsonObj)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	_, _ = w.Write([]byte(text))
}

// Text writes a plain-text (or JSON-wrapped) message with no structured body.
func Text(w http.ResponseWriter, r *http.Request, status int, text string) {
	Respond(w, r, status, map[string]string{"message": text}, text)
}

// errorBody is the JSON shape for an error response.
type errorBody struct {
	Error string `json:"error"`
	Hint  string `json:"hint,omitempty"`
}

// Error writes an instructive error. In text mode the status code appears in
// the body too, so agents that only see the body still learn what happened.
func Error(w http.ResponseWriter, r *http.Request, status int, code, hint string) {
	if WantJSON(r) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(errorBody{Error: code, Hint: hint})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	b := strings.Builder{}
	fmt.Fprintf(&b, "ERROR %s\n", code)
	if hint != "" {
		fmt.Fprintf(&b, "HINT  %s\n", hint)
	}
	_, _ = w.Write([]byte(b.String()))
}

// ---- plain-text field formatting ----------------------------------------

// Field is a key/value pair used to build labeled lines and records.
type Field struct {
	Key string
	Val string
}

// F constructs a Field.
func F(key, val string) Field { return Field{Key: key, Val: val} }

// Line renders a single grepable record line: "handle  key=val key=\"v2\"".
// Empty values are omitted. Values containing spaces are double-quoted.
func Line(handle string, fields ...Field) string {
	b := strings.Builder{}
	b.WriteString(handle)
	for _, f := range fields {
		if f.Val == "" {
			continue
		}
		b.WriteByte(' ')
		b.WriteByte(' ')
		b.WriteString(f.Key)
		b.WriteByte('=')
		b.WriteString(quote(f.Val))
	}
	return b.String()
}

// Record renders a multi-line "key: value" detail block. Empty values are
// omitted. Keys are padded to a common width for readability.
func Record(fields ...Field) string {
	width := 0
	for _, f := range fields {
		if f.Val == "" {
			continue
		}
		if len(f.Key) > width {
			width = len(f.Key)
		}
	}
	b := strings.Builder{}
	for _, f := range fields {
		if f.Val == "" {
			continue
		}
		fmt.Fprintf(&b, "%-*s  %s\n", width+1, f.Key+":", f.Val)
	}
	return strings.TrimRight(b.String(), "\n")
}

func quote(v string) string {
	if strings.ContainsAny(v, " \t\"") {
		return `"` + strings.ReplaceAll(v, `"`, `'`) + `"`
	}
	return v
}
