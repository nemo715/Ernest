package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/nemo715/Ernest/internal/config"
	"github.com/nemo715/Ernest/internal/eval"
)

// TestTemplatesScaffold verifies every `ernest new` template:
//   - cmdNew writes every declared file into a fresh dir
//   - ernest.json parses and passes config.Validate
//   - scenarios files (when present) parse as eval scenarios
//   - main.go (when present) compiles against the local module
//   - no placeholder text is left in any scaffolded file
func TestTemplatesScaffold(t *testing.T) {
	repoRoot := filepath.Dir(filepath.Dir(mustGetwd(t))) // <repo>/cmd/ernest -> <repo>
	names := make([]string, 0, len(newTemplates))
	for n := range newTemplates {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			tpl, ok := newTemplates[name]
			if !ok {
				t.Fatalf("template %q missing from newTemplates", name)
			}
			if tpl.summary == "" {
				t.Error("template has no summary")
			}
			dir := t.TempDir()
			if err := cmdNew([]string{name, dir}); err != nil {
				t.Fatalf("cmdNew: %v", err)
			}

			// Every declared file must exist and be non-empty.
			for f := range tpl.files {
				path := filepath.Join(dir, filepath.FromSlash(f))
				st, err := os.Stat(path)
				if err != nil {
					t.Fatalf("file %s not scaffolded: %v", f, err)
				}
				if st.Size() == 0 {
					t.Errorf("file %s is empty", f)
				}
			}

			// ernest.json must pass config.Validate.
			if _, err := config.Load(filepath.Join(dir, "ernest.json")); err != nil {
				t.Fatalf("config.Load: %v", err)
			}

			// Scenarios files must parse.
			for _, sc := range []string{"scenarios.json", "scenarios-quantum.json"} {
				if _, ok := tpl.files[sc]; !ok {
					continue
				}
				if scs, err := eval.LoadScenarios(filepath.Join(dir, sc)); err != nil {
					t.Fatalf("scenarios %s: %v", sc, err)
				} else if len(scs) == 0 {
					t.Fatalf("scenarios %s: empty", sc)
				}
			}

			// No placeholder text in any scaffolded file.
			for f, content := range tpl.files {
				low := strings.ToLower(content)
				for _, tok := range []string{"todo", "fixme", "replace_me", "placeholder", "your_name", "xxx"} {
					if strings.Contains(low, tok) {
						t.Errorf("file %s contains placeholder token %q", f, tok)
					}
				}
			}

			// main.go (when present) must compile against the local module.
			if _, hasMain := tpl.files["main.go"]; !hasMain {
				return
			}
			compileScaffold(t, dir, repoRoot)
		})
	}
}

// TestNewUsageListsTemplates checks `ernest new` (no args) advertises
// every template, including the knowledge and quantum additions.
func TestNewUsageListsTemplates(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	cmdErr := cmdNew(nil)
	_ = w.Close()
	os.Stdout = old
	if cmdErr != nil {
		t.Fatal(cmdErr)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for n := range newTemplates {
		if !strings.Contains(out, n) {
			t.Errorf("usage missing template %q:\n%s", n, out)
		}
	}
}

// compileScaffold builds the scaffolded module with a replace directive
// pointing at the local ernest checkout (no network, no published module).
func compileScaffold(t *testing.T, dir, repoRoot string) {
	t.Helper()
	mod := "module myapp\n\ngo 1.26.5\n\nrequire github.com/nemo715/Ernest v0.1.7\n\n" +
		"replace github.com/nemo715/Ernest => " + filepath.ToSlash(repoRoot) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
		t.Fatal(err)
	}
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goBin += ".exe"
	}
	cmd := exec.Command(goBin, "build", "./...")
	cmd.Dir = dir
	// -mod=mod lets the build resolve transitive deps of the local ernest
	// module and update go.mod/go.sum from the module cache (no network).
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scaffold build: %v\n%s", err, out)
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}
