package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"path"
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

// killTimeout bounds ending a scan's environments: the worker answers a kill
// at once, and one that does not must not hold Create.
const killTimeout = 5 * time.Second

// agents is the body of the agents system record, written once at the start
// of every file: for every drive a session worker mounts, and for every
// worker's scratch, the root AGENTS.md read and the ones below it listed. A
// drive is scanned on the worker serving it, where a listing takes
// milliseconds and not the seconds it takes over the mesh, and its paths
// start at the drive's name; the workers record says where each worker
// mounts it. The calls show verbatim with the worker they ran on, so the
// model knows what ran and how to run more. The scans run at once, each
// under agentsTimeout, and print in a fixed order.
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
				scans = append(scans, func(ctx context.Context) string { return h.drive(ctx, id, d.Name) })
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
	for name := range seen { // the session has no environment to keep on a server outside it
		s, _, ok := h.server(name)
		if ok && !slices.ContainsFunc(conf.Workers, func(w statefile.Worker) bool { return w.Name == s.Name }) {
			kill(ctx, s, id)
		}
	}
	return "The following read and run calls were made automatically\n" + strings.Join(out, "")
}

// scratch is the section of a worker's scratch, which only the worker knows.
func (h *Harness) scratch(ctx context.Context, id, name string) string {
	r, ok := h.Workers.Get(name)
	if !ok {
		return fmt.Sprintf("\nworker %s, scratch: not connected\n", name)
	}
	return scan(ctx, r, id, fmt.Sprintf("worker %s, scratch %s", name, r.Scratch), r.Scratch, listing(r.Interps[0], r.Scratch))
}

// drive is the section of the drive named name, scanned on the worker
// serving it. The listing runs from the drive's parent, so it prints paths
// that start at the drive's name: a server keeps a drive in a directory of
// that name.
func (h *Harness) drive(ctx context.Context, id, name string) string {
	s, root, ok := h.server(name)
	if !ok {
		return fmt.Sprintf("\ndrive %s: no connected worker serves it\n", name)
	}
	interp, dir := s.Interps[0], path.Dir(root)
	code := fmt.Sprintf("(cd %s && %s)", quote(interp, dir), listing(interp, path.Base(root)))
	if interp == "pwsh" {
		code = fmt.Sprintf("Push-Location -LiteralPath %s; %s; Pop-Location", quote(interp, dir), listing(interp, path.Base(root)))
	}
	return scan(ctx, s, id, fmt.Sprintf("worker %s, which serves drive %s from %s", s.Name, name, root), root, code)
}

// scan is one section: the read of root/AGENTS.md and the listing code,
// both on r.
func scan(ctx context.Context, r *Remote, id, head, root, code string) string {
	var b strings.Builder
	file := root + "/AGENTS.md"
	fmt.Fprintf(&b, "\n%s\nread %s\n", head, file)
	rec, cut := call(ctx, r, id, "read", args{Path: file})
	b.WriteString(text("read", args{Path: file}, rec, cut))
	interp := r.Interps[0]
	fmt.Fprintf(&b, "run %s: %s\n", interp, code)
	rec, cut = call(ctx, r, id, "run", args{Interpreter: interp, Code: code})
	b.WriteString(text("run", args{}, rec, cut))
	return b.String()
}

// server is the connected worker serving the drive named name, and the path
// it serves the drive from.
func (h *Harness) server(name string) (*Remote, string, bool) {
	for _, l := range h.Workers.List() {
		for _, d := range l.Drives {
			if d.Name == name && d.Serve && d.State == worker.Serving {
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
			kill(ctx, r, id)
		}
	}
	return rec, cut
}

// kill ends the session's environments on r, under killTimeout even when ctx
// is done.
func kill(ctx context.Context, r *Remote, id string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), killTimeout)
	defer cancel()
	if err := r.Kill(ctx, id); err != nil {
		log.Printf("session %s: kill on %s: %v", id, r.Name, err)
	}
}

func text(tool string, a args, rec statefile.Record, cut int) string {
	return string(bytes.TrimRight(finish(tool, a, rec, cut).Body, "\n")) + "\n"
}

// listing prints every AGENTS.md under root to agentsDepth, in the
// interpreter's own language so the model can rerun or deepen it.
func listing(interp, root string) string {
	if interp == "pwsh" {
		return fmt.Sprintf("Get-ChildItem -LiteralPath %s -Recurse -Depth %d -Filter AGENTS.md -Name", quote(interp, root), agentsDepth-1)
	}
	return fmt.Sprintf("find %s -maxdepth %d -name AGENTS.md", quote(interp, root), agentsDepth)
}

// quote quotes s for the interpreter's language, so an apostrophe in a path
// stays in the path.
func quote(interp, s string) string {
	if interp == "pwsh" {
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
