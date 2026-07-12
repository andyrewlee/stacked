// Command st is stacked: a minimal, dependency-free, login-free CLI for managing
// stacked diffs on top of git. Stack metadata is stored locally in a JSON file
// inside the repository's git directory; it requires no login and talks to no
// host API.
package main

import (
	"os"

	"github.com/andyrewlee/stacked/cmd"
)

func main() {
	os.Exit(cmd.Execute())
}
