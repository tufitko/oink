// Package diag — runtime diagnostics for a pipeline run: pprof HTTP server,
// CPU/heap profiles and the runtime execution trace. Used by the CLI flags
// and available to embedding applications directly.
package diag

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	httppprof "net/http/pprof"
	"os"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"time"
)

// Config selects which diagnostics to enable. Zero value enables nothing.
type Config struct {
	// PprofAddr starts an HTTP server with /debug/pprof/ endpoints on the
	// given address (e.g. "localhost:6060"; port 0 picks a free one).
	PprofAddr string
	// CPUProfile writes a CPU profile to the given file for the whole session.
	CPUProfile string
	// MemProfile writes a heap profile to the given file on Stop.
	MemProfile string
	// Trace writes the runtime execution trace to the given file
	// (inspect with `go tool trace`).
	Trace string
}

// Session holds the active diagnostics; stop it with Stop.
type Session struct {
	// PprofAddr is the actual address of the pprof server ("" if not started).
	PprofAddr string

	memProfile string
	server     *http.Server
	cpuFile    *os.File
	traceFile  *os.File
}

// Start enables the configured diagnostics. On error everything already
// started is stopped.
func Start(cfg Config) (*Session, error) {
	s := &Session{memProfile: cfg.MemProfile}

	if cfg.PprofAddr != "" {
		ln, err := net.Listen("tcp", cfg.PprofAddr)
		if err != nil {
			return nil, fmt.Errorf("pprof listen: %w", err)
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/debug/pprof/", httppprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", httppprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", httppprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", httppprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", httppprof.Trace)
		s.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		s.PprofAddr = ln.Addr().String()
		go func() { _ = s.server.Serve(ln) }()
	}

	if cfg.CPUProfile != "" {
		f, err := os.Create(cfg.CPUProfile)
		if err != nil {
			_ = s.Stop()
			return nil, fmt.Errorf("create cpu profile: %w", err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			_ = f.Close()
			_ = s.Stop()
			return nil, fmt.Errorf("start cpu profile: %w", err)
		}
		s.cpuFile = f
	}

	if cfg.Trace != "" {
		f, err := os.Create(cfg.Trace)
		if err != nil {
			_ = s.Stop()
			return nil, fmt.Errorf("create trace file: %w", err)
		}
		if err := trace.Start(f); err != nil {
			_ = f.Close()
			_ = s.Stop()
			return nil, fmt.Errorf("start trace: %w", err)
		}
		s.traceFile = f
	}

	return s, nil
}

// Stop flushes and disables everything that was started. Safe to call once.
func (s *Session) Stop() error {
	var errs []error

	if s.traceFile != nil {
		trace.Stop()
		if err := s.traceFile.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close trace file: %w", err))
		}
		s.traceFile = nil
	}

	if s.cpuFile != nil {
		pprof.StopCPUProfile()
		if err := s.cpuFile.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close cpu profile: %w", err))
		}
		s.cpuFile = nil
	}

	if s.memProfile != "" {
		if err := writeHeapProfile(s.memProfile); err != nil {
			errs = append(errs, err)
		}
		s.memProfile = ""
	}

	if s.server != nil {
		if err := s.server.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close pprof server: %w", err))
		}
		s.server = nil
	}

	return errors.Join(errs...)
}

func writeHeapProfile(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create mem profile: %w", err)
	}
	defer f.Close()
	runtime.GC() // get up-to-date allocation statistics
	if err := pprof.WriteHeapProfile(f); err != nil {
		return fmt.Errorf("write mem profile: %w", err)
	}
	return nil
}
