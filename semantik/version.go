package semantik

import (
	"fmt"
	"runtime"
)

// Version is the semantic version of this SDK release. It is included
// in every outgoing User-Agent header and is available for callers
// that want to log or report the running SDK revision.
const Version = "0.2.0"

// UserAgent builds the User-Agent header value embedded by every
// request. The format is stable: callers are free to grep server logs
// for it. Tooling that talks to Semantik directly (debug clients,
// load generators) should use this so server logs see one wire-level
// identity for "the Go client family".
//
//	noetive-sdk-go/<Version> (go<runtime>; <goos>/<goarch>)
func UserAgent() string {
	return fmt.Sprintf("noetive-sdk-go/%s (%s; %s/%s)",
		Version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
