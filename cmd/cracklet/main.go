// Command cracklet runs Firecracker microVMs on Apple Silicon Macs.
package main

import (
	"os"

	"github.com/itlabs-gmbh/cracklet/internal/cli"
)

func main() {
	os.Exit(cli.Main())
}
