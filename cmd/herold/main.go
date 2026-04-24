// Command herold is the single-binary entrypoint: it runs the server and
// hosts the CLI subcommands. See internal/admin for the cobra command tree.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/hanshuebner/herold/internal/admin"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	os.Exit(admin.Execute(ctx))
}
