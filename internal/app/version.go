package app

import "fmt"

var (
	appVersion = "dev"
	gitCommit  = "unknown"
	buildTime  = "unknown"
)

func buildVersionSummary() string {
	return fmt.Sprintf("version=%s commit=%s build_time=%s", appVersion, gitCommit, buildTime)
}
