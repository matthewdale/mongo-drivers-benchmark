package workload

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Load reads a workload YAML file, applies defaults, expands @path references,
// and validates required fields.
func Load(path string) (*Workload, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading workload file: %w", err)
	}
	var w Workload
	if err := yaml.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("parsing workload YAML: %w", err)
	}

	dir := filepath.Dir(path)

	// Apply defaults.
	if w.Database == "" {
		w.Database = "perftest"
	}
	if w.Run.Iterations == 0 {
		w.Run.Iterations = 100
	}
	if w.Run.Concurrency == 0 {
		w.Run.Concurrency = 1
	}

	// Expand @path references in all body fields.
	if err := expandBody(w.Body, dir); err != nil {
		return nil, fmt.Errorf("body: %w", err)
	}
	for i := range w.Setup {
		if err := expandBody(w.Setup[i].Body, dir); err != nil {
			return nil, fmt.Errorf("setup[%d].body: %w", i, err)
		}
	}
	for i := range w.SetupPerIter {
		if err := expandBody(w.SetupPerIter[i].Body, dir); err != nil {
			return nil, fmt.Errorf("setupPerIteration[%d].body: %w", i, err)
		}
	}

	// Validate required fields.
	if w.Name == "" {
		return nil, fmt.Errorf("workload name is required")
	}
	if !ValidCommands[w.Command] {
		return nil, fmt.Errorf("unknown command %q; must be one of: find findOne insertOne insertMany updateOne updateMany deleteOne deleteMany bulkWrite clientBulkWrite", w.Command)
	}
	if w.Command != "clientBulkWrite" && w.Collection == "" {
		return nil, fmt.Errorf("collection is required for command %q", w.Command)
	}

	return &w, nil
}

// expandBody walks a RawBody and replaces any string value that starts with
// "@" with the JSON-parsed contents of the referenced file, resolved relative
// to dir.
func expandBody(body RawBody, dir string) error {
	for k, v := range body {
		s, ok := v.(string)
		if !ok || !strings.HasPrefix(s, "@") {
			continue
		}
		fpath := filepath.Join(dir, s[1:])
		raw, err := loadFixture(fpath)
		if err != nil {
			return fmt.Errorf("field %q: %w", k, err)
		}
		body[k] = raw
	}
	return nil
}

// loadFixture reads a .json or .ldjson file and returns its content as a
// json.RawMessage. For .ldjson, each non-empty line becomes one element of a
// JSON array.
func loadFixture(path string) (json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("loading fixture %q: %w", path, err)
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ldjson", ".jsonl":
		return parseLDJSON(data, path)
	default:
		if !json.Valid(data) {
			return nil, fmt.Errorf("fixture %q is not valid JSON", path)
		}
		return json.RawMessage(bytes.TrimSpace(data)), nil
	}
}

func parseLDJSON(data []byte, path string) (json.RawMessage, error) {
	var docs []json.RawMessage
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 16*1024*1024), 16*1024*1024)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if !json.Valid(line) {
			return nil, fmt.Errorf("fixture %q line %d: not valid JSON", path, lineNum)
		}
		docs = append(docs, json.RawMessage(append([]byte(nil), line...)))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning fixture %q: %w", path, err)
	}
	return json.Marshal(docs)
}

// BuildBody merges a workload's body, options, and namespace fields into a
// single JSON object ready to POST to the service.
func BuildBody(wl *Workload) (json.RawMessage, error) {
	return buildBodyFrom(wl.Body, wl.Options, wl.Database, wl.Collection, wl.Command)
}

// BuildStepBody merges a setup step's body with the workload's namespace.
// If the step already specifies database/collection, those values are preserved.
func BuildStepBody(step *Step, wl *Workload) (json.RawMessage, error) {
	m := make(RawBody, len(step.Body)+2)
	for k, v := range step.Body {
		m[k] = v
	}
	if wl.Collection != "" {
		if _, ok := m["database"]; !ok {
			m["database"] = wl.Database
		}
		if _, ok := m["collection"]; !ok {
			m["collection"] = wl.Collection
		}
	}
	return json.Marshal(m)
}

func buildBodyFrom(body, options RawBody, database, collection, command string) (json.RawMessage, error) {
	m := make(RawBody, len(body)+3)
	for k, v := range body {
		m[k] = v
	}
	if command != "clientBulkWrite" {
		m["database"] = database
		m["collection"] = collection
	}
	if len(options) > 0 {
		m["options"] = options
	}
	return json.Marshal(m)
}
