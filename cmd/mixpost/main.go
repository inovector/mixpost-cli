// Command mixpost is the Mixpost command line client.
package main

import (
	"os"

	"github.com/inovector/mixpost-cli/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
