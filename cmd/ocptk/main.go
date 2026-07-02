package main

import (
	"fmt"
	"os"

	"github.com/GhislainAdon/ocp-toolkit/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Erreur: %v\n", err)
		os.Exit(1)
	}
}
