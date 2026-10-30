package main

import (
	"fmt"
	"github.com/resident-labs/dyna/contract/internal/contractgen"
	"os"
)

func main() {
	if err := contractgen.Generate(".", "."); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
