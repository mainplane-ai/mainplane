package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/statefile"
	"github.com/mainplane-ai/mainplane/pkg/worker"
)

// agentsDepth bounds the AGENTS.md listing: root/repos/<repo>/AGENTS.md is
// three levels down, and a full walk of a drive holding a node_modules is not.
const agentsDepth = 3

// agentsTimeout bounds each scan, since Create waits for the slowest one. A
// local listing takes milliseconds; a scan still going after this is stuck on
// a worker or a drive, and the record says which.
const agentsTimeout = 30 * time.Second

// agents is the body of the agents system record, written once at the start
// of every file: for every drive, as the first worker that has it mounted
// sees it, and for every worker's scratch, the root AGENTS.md read and the
// ones below it listed. It runs the model's own tools and shows the calls
// verbatim, so the model knows what ran and how to run more. The scans run
// at once, each under agentsTimeout, and print in a fixed order.
func (h *Harness) agents(ctx context.Context, id string, conf statefile.Conf) string {
	var scans []func(context.Context) string
	seen := map[string]bool{}
	for _, w := range conf.Workers {
		r, ok := h.Workers.Get(w.Name)
		if !ok {
			continue
		}
		for _, d := range r.Drives() {
			if !d.Serve && d.State == worker.Mounted && !seen[d.Name] {
				seen[d.Name] = true
				scans = append(scans, func(ctx context.Context) string { return h.drive(ctx, id, conf, r, d) })
			}
		}
	}
	for _, w := range conf.Workers {
		scans = append(scans, func(ctx context.Context) string { return h.scratch(ctx, id, w.Name) })
	}
	out := make([]string, len(scans))
	var wg sync.WaitGroup
	for i, scan := range scans {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, agentsTimeout)
			defer cancel()
			out[i] = scan(ctx)
		})
	}
	wg.Wait()
	return "The following read and run calls were made automatically\n" + strings.Join(out, "")
}

// scratch is the section of a worker's scratch, which only the worker knows.
func (h *Harness) scratch(ctx context.Context, id, name string) string {
	r, ok := h.Workers.Get(name)
	if !ok {
		return fmt.Sprintf("\nworker %s, scratch: not connected\n", name)
	}
	var b strings.Builder
	path := r.Scratch + "/AGENTS.md"
	fmt.Fprintf(&b, "\nworker %s, scratch %s\nread %s\n", name, r.Scratch, path)
	rec, cut := call(ctx, r, id, "read", args{Path: path})
	b.WriteString(text("read", args{Path: path}, rec, cut))
	interp, code := r.Interps[0], listing(r.Interps[0], r.Scratch)
	fmt.Fprintf(&b, "run %s: %s\n", interp, code)
	rec, cut = call(ctx, r, id, "run", args{Interpreter: interp, Code: code})
	b.WriteString(text("run", args{}, rec, cut))
	return b.String()
}

// drive is the section of drive d as worker m, which mounts it, sees it. The
// listing runs on the worker serving d: over the mesh a recursive listing
// takes seconds, on the disk milliseconds. It prints as m's own listing would,
// under m's mount path, so the call shown is still the one to rerun. The read
// of one file stays on m, so its result, an error too, is m's own.
func (h *Harness) drive(ctx context.Context, id string, conf statefile.Conf, m *Remote, d worker.Drive) string {
	var b strings.Builder
	path := d.Path + "/AGENTS.md"
	fmt.Fprintf(&b, "\nworker %s, drive %s %s\nread %s\n", m.Name, d.Name, d.Path, path)
	rec, cut := call(ctx, m, id, "read", args{Path: path})
	b.WriteString(text("read", args{Path: path}, rec, cut))
	interp := m.Interps[0]
	fmt.Fprintf(&b, "run %s: %s\n", interp, listing(interp, d.Path))
	s, root, ok := h.server(d.Name)
	if !ok {
		fmt.Fprintf(&b, "error: no connected worker serves drive %s\n", d.Name)
		return b.String()
	}
	a := args{Interpreter: s.Interps[0], Code: listing(s.Interps[0], root)}
	rec, cut = call(ctx, s, id, "run", a)
	if !slices.ContainsFunc(conf.Workers, func(w statefile.Worker) bool { return w.Name == s.Name }) {
		_ = s.Kill(context.WithoutCancel(ctx), id) // the session has no environment there to keep
	}
	if rec.Exit != nil && *rec.Exit == 0 && cut == 0 {
		rec.Body = relist(rec.Body, a.Interpreter, root, interp, d.Path)
	} else {
		fmt.Fprintf(&b, "listed on worker %s, which serves the drive: %s %s\n", s.Name, a.Interpreter, a.Code)
	}
	b.WriteString(text("run", args{}, rec, cut))
	return b.String()
}

// server is the connected worker serving the drive named name, and the path
// it serves the drive from.
func (h *Harness) server(name string) (*Remote, string, bool) {
	for _, l := range h.Workers.List() {
		for _, d := range l.Drives {
			if d.Name == name && d.Serve {
				r, ok := h.Workers.Get(l.Name)
				return r, d.Path, ok
			}
		}
	}
	return nil, "", false
}

// call executes one call of a scan. A call over agentsTimeout says so, and a
// run is ended on the worker, else it would hold the session's environment
// there until runTimeout.
func call(ctx context.Context, r *Remote, id, tool string, a args) (statefile.Record, int) {
	rec, cut := execute(ctx, r, id, tool, a, nil)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		rec.Exit, rec.Body = nil, fmt.Appendf(nil, "error: timed out after %s on worker %s, this scan is incomplete", agentsTimeout, r.Name)
		if tool == "run" {
			_ = r.Kill(context.WithoutCancel(ctx), id)
		}
	}
	return rec, cut
}

func text(tool string, a args, rec statefile.Record, cut int) string {
	return string(bytes.TrimRight(finish(tool, a, rec, cut).Body, "\n")) + "\n"
}

// relist turns the output of listing in interpreter from under root into
// the output of listing in interpreter to under toRoot. pwsh -Name prints
// paths relative to the root with backslashes, find prints whole paths. The
// order is pwsh's on Windows, a directory's own file before its
// subdirectories, these by name in any case. Lines that are not paths stay.
func relist(out []byte, from, root, to, toRoot string) []byte {
	var rels [][]string
	var lines []string
	for l := range strings.Lines(string(out)) {
		l = strings.TrimRight(l, "\r\n")
		rel, ok := strings.CutPrefix(l, root+"/")
		if from == "pwsh" {
			rel, ok = strings.ReplaceAll(l, `\`, "/"), strings.HasSuffix(l, "AGENTS.md")
		}
		if !ok {
			lines = append(lines, l)
			continue
		}
		rels = append(rels, strings.Split(rel, "/"))
	}
	slices.SortFunc(rels, func(a, b []string) int {
		return slices.CompareFunc(a[:len(a)-1], b[:len(b)-1], func(x, y string) int { return strings.Compare(strings.ToUpper(x), strings.ToUpper(y)) })
	})
	for _, rel := range rels {
		if to == "pwsh" {
			lines = append(lines, strings.Join(rel, `\`))
		} else {
			lines = append(lines, toRoot+"/"+strings.Join(rel, "/"))
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

// listing prints every AGENTS.md under root to agentsDepth, in the
// interpreter's own language so the model can rerun or deepen it. Root is
// quoted for that language, so an apostrophe in a path stays in the path.
func listing(interp, root string) string {
	if interp == "pwsh" {
		return fmt.Sprintf("Get-ChildItem -LiteralPath '%s' -Recurse -Depth %d -Filter AGENTS.md -Name", strings.ReplaceAll(root, "'", "''"), agentsDepth-1)
	}
	return fmt.Sprintf("find '%s' -maxdepth %d -name AGENTS.md", strings.ReplaceAll(root, "'", `'\''`), agentsDepth)
}
