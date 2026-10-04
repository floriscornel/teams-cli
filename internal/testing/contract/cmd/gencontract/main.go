// Command gencontract regenerates the committed Layer 6 contract artifacts
// (the trimmed OpenAPI description, the route list and the provenance record)
// from a local, gitignored refs/ mirror. Do not run it by hand: use
//
//	scripts/gen-contract.sh
//
// which documents the inputs, the environment and the trim rules.
//
// The heavy lifting lives in the contract package's Generate so that a test can
// drive it as well (TestCommittedArtifactsAreCurrent), which keeps this wrapper
// thin enough for the repo-wide coverage floor to exclude it.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/floriscornel/teams-cli/internal/testing/contract"
)

func main() {
	root := flag.String("root", ".", "repository root containing refs/")
	list := flag.Bool("list", false, "print the generated route list and exit")
	stdout := flag.Bool("stdout", false, "print the generated trimmed spec to stdout instead of writing files")
	flag.Parse()

	generated, err := contract.Generate(*root)
	if err != nil {
		os.Stderr.WriteString("gen-contract: " + err.Error() + "\n")
		os.Exit(1)
	}

	switch {
	case *list:
		if _, err := os.Stdout.Write(generated.Routes); err != nil {
			os.Stderr.WriteString("gen-contract: " + err.Error() + "\n")
			os.Exit(1)
		}
	case *stdout:
		if _, err := os.Stdout.Write(contract.Spec(generated)); err != nil {
			os.Stderr.WriteString("gen-contract: " + err.Error() + "\n")
			os.Exit(1)
		}
	default:
		written, err := contract.WriteArtifacts(*root, generated)
		if err != nil {
			os.Stderr.WriteString("gen-contract: " + err.Error() + "\n")
			os.Exit(1)
		}
		for _, path := range written {
			info, err := os.Stat(path)
			if err != nil {
				os.Stderr.WriteString("gen-contract: " + err.Error() + "\n")
				os.Exit(1)
			}
			rel, err := filepath.Rel(*root, path)
			if err != nil {
				rel = path
			}
			fmt.Printf("wrote %s (%d bytes)\n", rel, info.Size())
		}
	}
}
