// Package mailwatch wakes a waiter when a message file lands in a queue folder
// or a named file changes. A wake is only a hint: kqueue learns new names by
// rescanning a folder, so a burst can merge or lose one. The caller still lists
// its queues after every wake and keeps its own timeout.
package mailwatch

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// Wake says why the watch woke: Kind is "queued" for a message file in a
// queue, or the kind given to AddFile.
type Wake struct{ Kind, Path string }

// Watch is one fsnotify watcher and the rules for what wakes it.
type Watch struct {
	fw     *fsnotify.Watcher
	mu     sync.Mutex
	queues map[string]bool
	files  map[string]string
	wakes  chan Wake
	errs   chan error
	once   sync.Once
}

// New opens the watcher. Nothing wakes it until a queue or file is added.
func New() (*Watch, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watch{fw: fw, queues: map[string]bool{}, files: map[string]string{},
		wakes: make(chan Wake, 1), errs: make(chan error, 1)}
	go w.loop()
	return w, nil
}

// AddQueue wakes on a message file created in dir: a name that ends in .json
// and does not start with a dot, the bridge's own *.json glob. Writers rename
// x.json.tmp to x.json, and the bridge claims it as x.json.taken; neither of
// those names wakes it.
func (w *Watch) AddQueue(dir string) error {
	dir = filepath.Clean(dir)
	w.mu.Lock()
	w.queues[dir] = true
	w.mu.Unlock()
	return w.fw.Add(dir)
}

// AddFile wakes with kind on any event naming path. It watches path's folder,
// so the file need not exist yet.
func (w *Watch) AddFile(path, kind string) error {
	path = filepath.Clean(path)
	w.mu.Lock()
	w.files[path] = kind
	w.mu.Unlock()
	return w.fw.Add(filepath.Dir(path))
}

// Waiting returns the first message file already in a queue. Called after
// the queues are added, it closes the gap between the caller's last scan and
// the watch starting.
func (w *Watch) Waiting() (string, bool) {
	w.mu.Lock()
	dirs := make([]string, 0, len(w.queues))
	for d := range w.queues {
		dirs = append(dirs, d)
	}
	w.mu.Unlock()
	sort.Strings(dirs)
	for _, d := range dirs {
		ents, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.Type().IsRegular() && isMessage(e.Name()) {
				return filepath.Join(d, e.Name()), true
			}
		}
	}
	return "", false
}

// Wakes delivers at most one pending wake; one is all a waiter needs.
func (w *Watch) Wakes() <-chan Wake { return w.wakes }

// Errors delivers fsnotify's errors, such as an event queue overflow.
func (w *Watch) Errors() <-chan error { return w.errs }

// Close stops the watcher and its goroutine. It is safe to call twice.
func (w *Watch) Close() error {
	var err error
	w.once.Do(func() { err = w.fw.Close() })
	return err
}

func isMessage(name string) bool {
	return strings.HasSuffix(name, ".json") && !strings.HasPrefix(name, ".")
}

func (w *Watch) loop() {
	for {
		select {
		case ev, ok := <-w.fw.Events:
			if !ok {
				return
			}
			if wk, hit := w.classify(ev); hit {
				select {
				case w.wakes <- wk:
				default:
				}
			}
		case err, ok := <-w.fw.Errors:
			if !ok {
				return
			}
			select {
			case w.errs <- err:
			default:
			}
		}
	}
}

// content is every op that can mean a file appeared, changed or went away.
// A bare CHMOD is left out: macOS touches a new file's attributes shortly
// after it is created (seen live on the nudge, up to ~250 ms later), which
// would wake a watch armed just after its own caller wrote that file.
const content = fsnotify.Create | fsnotify.Write | fsnotify.Remove | fsnotify.Rename

func (w *Watch) classify(ev fsnotify.Event) (Wake, bool) {
	if ev.Op&content == 0 {
		return Wake{}, false
	}
	name := filepath.Clean(ev.Name)
	w.mu.Lock()
	defer w.mu.Unlock()
	if kind, ok := w.files[name]; ok {
		return Wake{Kind: kind, Path: name}, true
	}
	if w.queues[filepath.Dir(name)] && ev.Has(fsnotify.Create) && isMessage(filepath.Base(name)) {
		return Wake{Kind: "queued", Path: name}, true
	}
	return Wake{}, false
}
