package workload

// RawBody is an untyped map decoded from YAML. String values beginning with
// "@" are replaced by json.RawMessage after @path expansion in loader.go.
type RawBody = map[string]any

// Workload is the schema for a single workload YAML file.
type Workload struct {
	Name         string    `yaml:"name"`
	Description  string    `yaml:"description"`
	Command      string    `yaml:"command"`
	Collection   string    `yaml:"collection"`
	Database     string    `yaml:"database"`
	Setup        []Step    `yaml:"setup"`
	SetupPerIter []Step    `yaml:"setupPerIteration"`
	Body         RawBody   `yaml:"body"`
	Options      RawBody   `yaml:"options"`
	Run          RunConfig `yaml:"run"`
	DatasetBytes int64     `yaml:"datasetBytes"`
}

// Step is one command+body pair used in setup sequences.
type Step struct {
	Command string  `yaml:"command"`
	Body    RawBody `yaml:"body"`
}

// RunConfig controls how many iterations to collect and at what concurrency.
type RunConfig struct {
	Iterations      int `yaml:"iterations"`
	MinDurationSecs int `yaml:"minDurationSecs"`
	Concurrency     int `yaml:"concurrency"`
}

// ValidCommands is the set of command names accepted by the spec.
var ValidCommands = map[string]bool{
	"find": true, "findOne": true,
	"insertOne": true, "insertMany": true,
	"updateOne": true, "updateMany": true,
	"deleteOne": true, "deleteMany": true,
	"bulkWrite": true, "clientBulkWrite": true,
}
