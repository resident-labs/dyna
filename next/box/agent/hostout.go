package agent

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/resident-labs/dyna/box/hostexec"
)

// hostOutRoot is where a host run's $DYNA_OUT files come back, in a folder named for the run.
const hostOutRoot = "/box/hostexec"

// extractHostOut writes the regular files of a host run's $DYNA_OUT tar under root/run and lists their box paths on out.
// Anything but a file inside the folder is skipped, so an archive cannot reach outside it.
func extractHostOut(archive io.Reader, root, run string, out io.Writer) error {
	dir := filepath.Join(root, run)
	var names []string
	tr := tar.NewReader(archive)
	for {
		head, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := path.Clean(head.Name)
		if head.Typeflag != tar.TypeReg || !filepath.IsLocal(name) {
			continue
		}
		target := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, io.LimitReader(tr, hostexec.OutputLimit))
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		names = append(names, target)
	}
	if len(names) > 0 {
		fmt.Fprintf(out, "\nFiles from $DYNA_OUT:\n%s\n", strings.Join(names, "\n"))
	}
	return nil
}
