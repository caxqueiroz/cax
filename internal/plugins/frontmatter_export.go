package plugins

// SplitFrontmatterForTest is the cross-package entry point that exposes
// splitFrontmatter to internal/creator's round-trip tests. Behavior identical
// to splitFrontmatter; this file exists to keep the public surface tight
// (callers outside tests should keep going through ReadCommands).
func SplitFrontmatterForTest(data []byte) (yamlSrc, body []byte) {
	return splitFrontmatter(data)
}
