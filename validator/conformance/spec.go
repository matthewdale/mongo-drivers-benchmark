// Package conformance is a black-box validator for services that implement
// the MongoDB drivers load-test HTTP API. It loads the spec from
// spec/openapi.yaml and exposes a Client + scenarios that exercise a target
// service end-to-end.
package conformance

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/pb33f/libopenapi"
	libval "github.com/pb33f/libopenapi-validator"
	"github.com/pb33f/libopenapi-validator/errors"
)

// Flags are package-level so they can be set via `go test -url=... -spec=...`.
var (
	flagURL  = flag.String("url", defaultURL(), "Base URL of the target service (e.g. http://localhost:8080).")
	flagSpec = flag.String("spec", defaultSpec(), "Path to spec/openapi.yaml.")
)

func defaultURL() string {
	if v := os.Getenv("MDBV_URL"); v != "" {
		return v
	}
	return ""
}

func defaultSpec() string {
	if v := os.Getenv("MDBV_SPEC"); v != "" {
		return v
	}
	// When the tests run from validator/conformance, the spec lives two
	// directories up. Tests resolve this lazily so the default works whether
	// invoked from the module root or from the package dir.
	return ""
}

// Spec wraps the parsed OpenAPI document and a response-body validator.
type Spec struct {
	Doc       libopenapi.Document
	Validator libval.Validator
}

// LoadSpec reads and parses an OpenAPI document at path. If path is empty
// LoadSpec searches the conventional locations (cwd, ../spec, ../../spec).
func LoadSpec(path string) (*Spec, error) {
	if path == "" {
		p, err := findSpec()
		if err != nil {
			return nil, err
		}
		path = p
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read spec: %w", err)
	}
	doc, err := libopenapi.NewDocument(raw)
	if err != nil {
		return nil, fmt.Errorf("parse spec: %w", err)
	}
	v, verrs := libval.NewValidator(doc)
	if len(verrs) > 0 {
		return nil, fmt.Errorf("build validator: %v", verrs)
	}
	return &Spec{Doc: doc, Validator: v}, nil
}

func findSpec() (string, error) {
	candidates := []string{
		"spec/openapi.yaml",
		"../spec/openapi.yaml",
		"../../spec/openapi.yaml",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			abs, err := filepath.Abs(c)
			if err != nil {
				return "", err
			}
			return abs, nil
		}
	}
	return "", fmt.Errorf("openapi.yaml not found; pass -spec=PATH")
}

// ValidateResponse validates resp's body against the schema the spec defines
// for (req.Method, req.URL.Path, resp.StatusCode). The body in resp.Body is
// drained and replaced with an equivalent reader so callers can still read it.
//
// Returns a slice of human-readable error strings (empty on success). Returns
// a nil slice and nil error when the response conforms.
func (s *Spec) ValidateResponse(req *http.Request, resp *http.Response) ([]string, error) {
	// libopenapi-validator reads resp.Body. Buffer it so the caller can read
	// it again afterwards.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))

	// Pass a fresh copy so the validator's read doesn't disturb the body we
	// just installed.
	respCopy := *resp
	respCopy.Body = io.NopCloser(bytes.NewReader(body))

	ok, verrs := s.Validator.ValidateHttpResponse(req, &respCopy)
	if ok {
		return nil, nil
	}
	out := make([]string, 0, len(verrs))
	for _, e := range verrs {
		out = append(out, formatValidationError(e))
	}
	return out, nil
}

func formatValidationError(e *errors.ValidationError) string {
	loc := e.SpecPath
	if loc == "" {
		loc = e.RequestPath
	}
	if e.Reason != "" {
		return fmt.Sprintf("%s: %s (%s)", loc, e.Message, e.Reason)
	}
	return fmt.Sprintf("%s: %s", loc, e.Message)
}

// pkgSpec is loaded once per test process.
var (
	pkgSpecOnce sync.Once
	pkgSpec     *Spec
	pkgSpecErr  error
)

// SharedSpec returns the package-level Spec, loading it on first call. The
// path comes from -spec= or the env or the search heuristic.
func SharedSpec() (*Spec, error) {
	pkgSpecOnce.Do(func() {
		pkgSpec, pkgSpecErr = LoadSpec(*flagSpec)
	})
	return pkgSpec, pkgSpecErr
}

// BaseURL returns the target service base URL from -url= or env.
func BaseURL() string {
	return *flagURL
}
