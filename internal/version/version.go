// Package version exposes build metadata injected by release builds.
package version

var (
	// Version is the semantic release version.
	Version = "dev"
	// Commit is the source revision.
	Commit = "unknown"
	// BuildTime is the reproducible build timestamp or "unknown".
	BuildTime = "unknown"
)

// Info is the machine-readable version response.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
}

// Current returns the active build metadata.
func Current() Info {
	return Info{Version: Version, Commit: Commit, BuildTime: BuildTime}
}
