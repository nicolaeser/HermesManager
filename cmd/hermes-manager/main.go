package main

import (
	"context"
	"fmt"
	"os"

	"github.com/nicolaeser/HermesManager/internal/app"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	application := app.New(app.BuildInfo{
		Version: version,
		Commit:  commit,
		Date:    date,
	})
	if err := application.Run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
