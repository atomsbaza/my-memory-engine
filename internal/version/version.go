// Package version holds the single source of truth for the engine version.
// It is consumed by the CLI (`mem --version`) and the MCP handshake
// (serverInfo.version) so both surfaces always report the same string.
package version

// Version is the semantic version of the engine binary. It is a constant on
// purpose: releases are cut by tagging (vX.Y.Z) and bumping this line in the
// same commit, which keeps the binary self-describing without ldflags.
const Version = "0.1.0"
