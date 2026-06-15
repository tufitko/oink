package diag

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestStartStopAllDiagnostics(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		PprofAddr:  "127.0.0.1:0",
		CPUProfile: filepath.Join(dir, "cpu.out"),
		MemProfile: filepath.Join(dir, "mem.out"),
		Trace:      filepath.Join(dir, "trace.out"),
	}

	s, err := Start(cfg)
	if err != nil {
		t.Fatal(err)
	}

	if s.PprofAddr == "" {
		t.Fatal("pprof server address is empty")
	}
	resp, err := http.Get("http://" + s.PprofAddr + "/debug/pprof/")
	if err != nil {
		t.Fatalf("pprof endpoint: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pprof endpoint status = %d, want 200", resp.StatusCode)
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	for _, path := range []string{cfg.CPUProfile, cfg.MemProfile, cfg.Trace} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if fi.Size() == 0 {
			t.Errorf("%s is empty", path)
		}
	}

	// The server must be down after Stop.
	if _, err := http.Get("http://" + s.PprofAddr + "/debug/pprof/"); err == nil {
		t.Error("pprof server still reachable after Stop")
	}
}

func TestStartNothing(t *testing.T) {
	s, err := Start(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if s.PprofAddr != "" {
		t.Fatalf("unexpected pprof addr %q", s.PprofAddr)
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestStartBadCPUProfilePath(t *testing.T) {
	_, err := Start(Config{CPUProfile: filepath.Join(t.TempDir(), "no-such-dir", "cpu.out")})
	if err == nil {
		t.Fatal("want error for unwritable cpu profile path")
	}
}
