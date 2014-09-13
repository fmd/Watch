package main

import (
	"errors"
	"flag"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/go-fsnotify/fsnotify"
)

var (
	debug     = flag.Bool("v", false, "Enable verbose debugging output")
	term      = flag.Bool("t", false, "Just run in the terminal (instead of an acme win)")
	exclude   = flag.String("x", "", "Exclude files and directories matching this regular expression")
	watchPath = flag.String("p", ".", "The path to watch")
)

var excludeRe *regexp.Regexp

const rebuildDelay = 200 * time.Millisecond

type ui interface {
	redisplay(func(io.Writer))
	// An empty struct is sent when the command should be rerun.
	rerun() <-chan struct{}
}

type writerUi struct{ io.Writer }

func (w writerUi) redisplay(f func(io.Writer)) { f(w) }

func (w writerUi) rerun() <-chan struct{} { return nil }

func main() {
	flag.Parse()

	ui := ui(writerUi{os.Stdout})
	if !*term {
		wd, err := os.Getwd()
		if err != nil {
			log.Fatalln("Failed to get the current directory")
		}
		if ui, err = newWin(wd); err != nil {
			log.Fatalln("Failed to open a win:", err)
		}
	}

	if *exclude != "" {
		var err error
		excludeRe, err = regexp.Compile(*exclude)
		if err != nil {
			log.Fatalln("Bad regexp: ", *exclude)
		}
	}

	timer := time.NewTimer(0)
	changes := startWatching(*watchPath)
	lastRun := time.Time{}
	lastChange := time.Now()

	for {
		select {
		case lastChange = <-changes:
			timer.Reset(rebuildDelay)

		case <-ui.rerun():
			lastRun = run(ui)

		case <-timer.C:
			if lastRun.Before(lastChange) {
				lastRun = run(ui)
			}
		}
	}
}

func run(ui ui) time.Time {
	ui.redisplay(func(out io.Writer) {
		cmd := exec.Command(flag.Arg(0), flag.Args()[1:]...)
		cmd.Stdout = out
		cmd.Stderr = out
		io.WriteString(out, strings.Join(flag.Args(), " ")+"\n")
		if err := cmd.Run(); err != nil {
			io.WriteString(out, err.Error()+"\n")
		}
		io.WriteString(out, time.Now().String()+"\n")
	})

	return time.Now()
}

func startWatching(p string) <-chan time.Time {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		panic(err)
	}

	switch isdir, err := isDir(p); {
	case err != nil:
		log.Fatalf("Failed to watch %s: %s", p, err)
	case isdir:
		watchDir(w, p)
	default:
		watch(w, p)
	}

	changes := make(chan time.Time)

	go sendChanges(w, changes)

	return changes
}

func sendChanges(w *fsnotify.Watcher, changes chan<- time.Time) {
	for {
		select {
		case err := <-w.Errors:
			log.Fatalf("Watcher error: %s\n", err)

		case ev := <-w.Events:
			time, err := modTime(ev.Name)
			if err != nil {
				log.Printf("Failed to get even time: %s", err)
				continue
			}

			debugPrint("%s at %s", ev, time)

			if ev.Op&fsnotify.Create != 0 {
				switch isdir, err := isDir(ev.Name); {
				case err != nil:
					log.Printf("Couldn't check if %s is a directory: %s", ev.Name, err)
					continue

				case isdir:
					watchDir(w, ev.Name)
				}
			}

			changes <- time
		}
	}
}

func modTime(p string) (time.Time, error) {
	switch s, err := os.Stat(p); {
	case os.IsNotExist(err):
		q := path.Dir(p)
		if q == p {
			err := errors.New("Failed to find directory for " + p)
			return time.Time{}, err
		}
		return modTime(q)

	case err != nil:
		return time.Time{}, err

	default:
		return s.ModTime(), nil
	}
}

func watchDir(w *fsnotify.Watcher, p string) {
	ents, err := ioutil.ReadDir(p)
	switch {
	case os.IsNotExist(err):
		return

	case err != nil:
		log.Printf("Failed to watch %s: %s", p, err)
	}

	for _, e := range ents {
		sub := path.Join(p, e.Name())
		if excludeRe != nil && excludeRe.MatchString(sub) {
			debugPrint("excluding %s", sub)
			continue
		}
		switch isdir, err := isDir(sub); {
		case err != nil:
			log.Printf("Failed to watch %s: %s", sub, err)

		case isdir:
			watchDir(w, sub)
		}
	}

	watch(w, p)
}

func watch(w *fsnotify.Watcher, p string) {
	debugPrint("Watching %s", p)

	switch err := w.Add(p); {
	case os.IsNotExist(err):
		debugPrint("%s no longer exists", p)

	case err != nil:
		log.Printf("Failed to watch %s: %s", p, err)
	}
}

func isDir(p string) (bool, error) {
	switch s, err := os.Stat(p); {
	case os.IsNotExist(err):
		return false, nil
	case err != nil:
		return false, err
	default:
		return s.IsDir(), nil
	}
}

func debugPrint(f string, vals ...interface{}) {
	if *debug {
		log.Printf("DEBUG: "+f, vals...)
	}
}
