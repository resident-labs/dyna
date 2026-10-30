package roomsvc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gzuidhof/tygo/tygo"
	"gopkg.in/yaml.v2"
)

func TestTygoConfigFilesMatchOutputs(t *testing.T) {
	files, err := filepath.Glob("../../tygo/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("find output configs: %v, files %d", err, len(files))
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var conf tygo.Config
		if err := yaml.Unmarshal(data, &conf); err != nil {
			t.Fatal(err)
		}
		if len(conf.Packages) == 0 || len(conf.TypeMappings) != 0 {
			t.Errorf("%s must contain packages and no shared mappings", file)
		}
		for _, pkg := range conf.Packages {
			if pkg.OutputPath != conf.Packages[0].OutputPath {
				t.Errorf("%s configures multiple outputs", file)
			}
			want := strings.TrimSuffix(filepath.Base(pkg.OutputPath), ".ts") + ".yaml"
			if filepath.Base(file) != want {
				t.Errorf("%s configures %s; want %s", file, pkg.OutputPath, want)
			}
		}
	}
}
