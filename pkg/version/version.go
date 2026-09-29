// Package version is the release a binary was built from. A release build
// stamps it: -ldflags "-X github.com/mainplane-ai/mainplane/pkg/version.V=v0.1.0".
// Every binary of one release carries the same version.
package version

// V is dev for every local build.
var V = "dev"

// wordmark heads what an install prints when it is done.
const wordmark = `            _           _
 _ __  __ _(_)_ _  _ __| |__ _ _ _  ___
| '  \/ _` + "`" + ` | | ' \| '_ \ / _` + "`" + ` | ' \/ -_)
|_|_|_\__,_|_|_||_| .__/_\__,_|_||_\___|
                  |_|
`

// Installed is the first thing a finished install prints.
func Installed() string { return wordmark + "mainplane " + V + " installed\n" }

// Match says whether a peer at version v may talk to this binary: both from
// one release, or either one a local build.
func Match(v string) bool { return v == V || v == "dev" || V == "dev" }
