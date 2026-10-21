package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/egrif/runnr/internal/engine"
)

// bgView is a background step the operator can watch.
type bgView struct {
	idx   int
	title string
	conn  *engine.BackgroundConn
}

// StartBackground gives a background step a terminal nobody has to watch;
// w at any prompt attaches to it (engine.BackgroundHost).
func (t *Terminal) StartBackground(idx int, title string) engine.TermConn {
	rows, cols := uint16(40), uint16(140)
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 && h > 0 {
		rows, cols = uint16(h), uint16(w)
	}
	c := engine.NewBackgroundConn(rows, cols)
	if t.bg == nil {
		t.bg = map[int]*bgView{}
	}
	t.bg[idx] = &bgView{idx: idx, title: title, conn: c}
	fmt.Fprintln(t.out, t.c("36", "  ◐ started in the background · press w at any prompt to watch it"))
	return c
}

// running lists background steps still going, by step number.
func (t *Terminal) running() []*bgView {
	var out []*bgView
	for _, b := range t.bg {
		select {
		case <-b.conn.Done():
		default:
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].idx < out[j].idx })
	return out
}

// watch attaches the terminal to a background step until Ctrl-] (or it ends).
func (t *Terminal) watch(ctx context.Context) error {
	bgs := t.running()
	if len(bgs) == 0 {
		fmt.Fprintln(t.out, t.c("2", "  (nothing is running in the background)"))
		return nil
	}
	b := bgs[0]
	if len(bgs) > 1 {
		for n, v := range bgs {
			fmt.Fprintf(t.out, "  %d) %s\n", n+1, v.title)
		}
		fmt.Fprint(t.out, "  watch which? ")
		line, err := t.readLine(ctx)
		if err != nil {
			return err
		}
		n, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || n < 1 || n > len(bgs) {
			return nil
		}
		b = bgs[n-1]
	}

	fmt.Fprintln(t.out, t.c("2", "  ── watching "+b.title+" · Ctrl-] to come back (Ctrl-C goes to it)"))
	fd := int(os.Stdin.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	// Ctrl-C belongs to what's being watched, and for a moment after
	// coming back (one meant for it can land just after).
	release := t.interrupts.hold("(Ctrl-C went to the background step you were watching)")
	defer func() {
		_ = term.Restore(fd, old)
		fmt.Fprintln(t.out, t.c("2", "\r\n  ── back in runnr"))
		time.AfterFunc(ttyGrace, release)
	}()
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		b.conn.SetSize(uint16(h), uint16(w))
	}
	recent, live, detach := b.conn.Attach()
	defer detach()
	os.Stdout.Write([]byte("\x1b[2J\x1b[H"))
	os.Stdout.Write(recent)

	keys := make(chan []byte)
	kctx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		for {
			k, err := kbd.next(kctx)
			if err != nil {
				return
			}
			select {
			case keys <- k:
			case <-kctx.Done():
				kbd.unread(k)
				return
			}
		}
	}()
	for {
		select {
		case p := <-live:
			os.Stdout.Write(p)
		case k := <-keys:
			if i := bytes.IndexByte(k, 0x1d); i >= 0 { // Ctrl-]
				b.conn.Input(k[:i])
				kbd.unread(k[i+1:])
				return nil
			}
			b.conn.Input(k)
		case <-b.conn.Done():
			fmt.Fprint(t.out, "\r\n"+t.c("2", "  (it has ended)"))
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ChooseWay asks which way to do a step (engine.Chooser); Enter takes
// the preferred one.
func (t *Terminal) ChooseWay(ctx context.Context, v engine.StepView, failed int) (int, error) {
	if failed >= 0 {
		fmt.Fprintln(t.out, t.c("1", "  Try it another way:"))
	} else {
		fmt.Fprintln(t.out, t.c("1", "  This step can be done different ways:"))
	}
	opts := map[string]string{"a": "a", "abort": "a"}
	def := ""
	for n, w := range v.Ways {
		note := map[string]string{"run": "scripted"}[w.Kind]
		if note == "" {
			note = w.Kind
		}
		if w.Preferred {
			note += ", preferred"
			def = strconv.Itoa(n + 1)
		}
		if n == failed {
			note += ", just failed"
		}
		fmt.Fprintf(t.out, "  %s %s %s\n", t.c("36;1", fmt.Sprintf("%d)", n+1)), t.c("1", w.Name), t.c("2", "("+note+")"))
		for _, l := range strings.Split(strings.TrimRight(w.Command, "\n"), "\n") {
			fmt.Fprintln(t.out, t.c("2", "       "+l))
		}
		opts[strconv.Itoa(n+1)] = strconv.Itoa(n + 1)
	}
	a, err := t.beforePrompt(ctx, v.Index, fmt.Sprintf("Which? [1-%d, Enter = %s] / [a]bort", len(v.Ways), def), opts, def)
	if err != nil {
		return 0, err
	}
	if a == "a" {
		return 0, engine.ErrAborted
	}
	n, _ := strconv.Atoi(a)
	return n - 1, nil
}
