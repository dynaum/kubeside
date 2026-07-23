// Package version holds build metadata stamped in at link time.
package version

var (
	// Version is the semantic version, set by GoReleaser.
	Version = "dev"
	// Commit is the git SHA, set by GoReleaser.
	Commit = "none"
	// Date is the build timestamp, set by GoReleaser.
	Date = "unknown"
)

// String renders the full build identity for --version and the UI footer.
func String() string {
	return Version + " (" + Commit + ", " + Date + ")"
}
