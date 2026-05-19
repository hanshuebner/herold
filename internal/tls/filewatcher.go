package tls

import (
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// WatchEntry describes a single file-backed certificate to be watched. The
// Hostname field determines how the reloaded certificate is registered:
//   - non-empty: store.Add(Hostname, cert)
//   - empty:     store.SetDefault(cert)
//
// SetFallback, when true, additionally calls store.SetDefault on a successful
// reload -- matching the buildTLSStore logic that promotes the first
// per-listener cert to the fallback position.
type WatchEntry struct {
	CertFile    string
	KeyFile     string
	Hostname    string // empty -> SetDefault only
	SetFallback bool   // also call SetDefault after Add
}

// FileWatcher watches cert+key file pairs for changes and hot-reloads them
// into a Store. It is started via StartFileWatcher and stopped by calling
// Stop() during server shutdown. See ADR-0001 and issue #110.
type FileWatcher struct {
	store    *Store
	entries  []WatchEntry
	logger   *slog.Logger
	watcher  *fsnotify.Watcher
	stopOnce sync.Once
	done     chan struct{}
}

// StartFileWatcher creates and starts a FileWatcher for the given entries. If
// entries is empty it returns nil without starting any goroutines. The
// returned watcher is stopped by calling Stop().
func StartFileWatcher(store *Store, entries []WatchEntry, logger *slog.Logger) (*FileWatcher, error) {
	if len(entries) == 0 {
		return nil, nil
	}

	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	// Watch the containing directory of every cert and key file so that
	// atomic-rename and symlink-swap patterns (certbot, Kubernetes
	// cert-manager) are caught -- a rename replaces the inode so an
	// inode-level watch would go stale.
	watchedDirs := make(map[string]bool)
	for _, e := range entries {
		for _, f := range []string{e.CertFile, e.KeyFile} {
			dir := filepath.Dir(f)
			if !watchedDirs[dir] {
				if addErr := fw.Add(dir); addErr != nil {
					fw.Close()
					return nil, addErr
				}
				watchedDirs[dir] = true
			}
		}
	}

	w := &FileWatcher{
		store:   store,
		entries: entries,
		logger:  logger,
		watcher: fw,
		done:    make(chan struct{}),
	}
	go w.run()
	return w, nil
}

// Stop shuts down the watcher goroutine. Safe to call more than once and safe
// to call on a nil receiver.
func (w *FileWatcher) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() {
		w.watcher.Close()
		<-w.done
	})
}

// debounceDelay is how long the watcher waits after the last event before
// attempting a reload. Cert and key are usually written as two events in quick
// succession; the delay lets both land before we try to parse the pair.
const debounceDelay = 100 * time.Millisecond

func (w *FileWatcher) run() {
	defer close(w.done)

	// pendingMap maps WatchEntry index to the time after which the entry
	// should be reloaded. Events reset the timer; a ticker drains entries
	// whose deadline has passed.
	type pendingEntry struct {
		deadline time.Time
	}
	pendingMap := make(map[int]pendingEntry)

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			for i, e := range w.entries {
				if event.Name == e.CertFile || event.Name == e.KeyFile {
					pendingMap[i] = pendingEntry{deadline: time.Now().Add(debounceDelay)}
				}
			}

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			w.logger.LogAttrs(nil, slog.LevelWarn, "tls filewatcher: watcher error",
				slog.String("err", err.Error()))

		case now := <-ticker.C:
			for i, p := range pendingMap {
				if now.After(p.deadline) {
					delete(pendingMap, i)
					w.reload(w.entries[i])
				}
			}
		}
	}
}

// reload attempts to re-read the cert+key pair for entry and, on success,
// updates the Store. On failure it logs the error and leaves the existing
// cert in place.
func (w *FileWatcher) reload(e WatchEntry) {
	cert, err := LoadFromFile(e.CertFile, e.KeyFile)
	if err != nil {
		w.logger.LogAttrs(nil, slog.LevelWarn, "tls filewatcher: reload failed; keeping previous certificate",
			slog.String("cert_file", e.CertFile),
			slog.String("key_file", e.KeyFile),
			slog.String("err", err.Error()))
		return
	}

	if e.Hostname != "" {
		w.store.Add(e.Hostname, cert)
		if e.SetFallback {
			w.store.SetDefault(cert)
		}
	} else {
		w.store.SetDefault(cert)
	}

	expiry := ""
	if cert.Leaf != nil {
		expiry = cert.Leaf.NotAfter.UTC().Format(time.RFC3339)
	}
	w.logger.LogAttrs(nil, slog.LevelInfo, "tls filewatcher: certificate reloaded",
		slog.String("cert_file", e.CertFile),
		slog.String("key_file", e.KeyFile),
		slog.String("hostname", e.Hostname),
		slog.String("not_after", expiry))
}
