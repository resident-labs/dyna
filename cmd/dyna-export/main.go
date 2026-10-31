// dyna-export exports a frozen v1 backup into the v2 bundle contract.
package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "dyna-export:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	verify := len(args) > 0 && args[0] == "verify"
	if verify {
		args = args[1:]
	}
	flags := flag.NewFlagSet("dyna-export", flag.ContinueOnError)
	data := flags.String("data", "", "captured v1 data directory")
	config := flags.String("config", "", "captured v1 config directory")
	backup := flags.String("backup", "", "standalone v1 SQLite backup")
	manifest := flags.String("manifest", "", "captured repository, ref, and skill manifest")
	out := flags.String("out", "", "new bundle directory")
	commit := flags.String("commit", "", "40-character source commit")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *data == "" || *config == "" || *backup == "" || *manifest == "" || *out == "" {
		return fmt.Errorf("require --data, --config, --backup, --manifest and --out")
	}
	if !verify && !commitPattern.MatchString(*commit) {
		return fmt.Errorf("export requires a full lowercase --commit SHA")
	}
	cap, err := readRepoManifest(*manifest)
	if err != nil {
		return err
	}
	db, err := openBackup(*backup)
	if err != nil {
		return err
	}
	defer db.Close()
	if verify {
		if err := verifyBundle(db, *out, *backup, cap); err != nil {
			return err
		}
		if err := verifyCapturedFiles(*data, *config, *out, cap); err != nil {
			return err
		}
		fmt.Println("Bundle counts match the captured inputs.")
		return nil
	}
	global, rooms, report, err := buildBundle(db, *backup, *data, *config, *commit, cap)
	if err != nil {
		return err
	}
	if err := writeBundle(*out, global, rooms); err != nil {
		return err
	}
	if err := writeReport(*out, report); err != nil {
		os.RemoveAll(*out)
		return err
	}
	fmt.Println("Bundle and field inventory written.")
	return nil
}
