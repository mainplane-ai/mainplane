package harness

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/mainplane-ai/mainplane/pkg/statefile"
	"github.com/mainplane-ai/mainplane/pkg/worker"
)

// agentsDepth bounds the AGENTS.md listing: root/repos/<repo>/AGENTS.md is
// three levels down, and a full walk of a drive holding a node_modules is not.
const agentsDepth = 3

// agents is the body of the agents system record, written once at the start
// of every file: for every drive, on the first worker that has it mounted,
// and for every worker's scratch, the root AGENTS.md read and the ones below
// it listed. It runs the model's own tools and shows the calls verbatim, so
// the model knows what ran and how to run more.
func (h *Harness) agents(ctx context.Context, id string, conf statefile.Conf) string {
	var b strings.Builder
	b.WriteString("The following read and run calls were made automatically\n")
	seen := map[string]bool{}
	for _, w := range conf.Workers {
		r, ok := h.Workers.Get(w.Name)
		if !ok {
			continue
		}
		for _, d := range r.Drives() {
			if !d.Serve && d.State == worker.Mounted && !seen[d.Name] {
				seen[d.Name] = true
				h.scan(ctx, &b, id, w.Name, "drive "+d.Name, d.Path)
			}
		}
	}
	for _, w := range conf.Workers {
		h.scan(ctx, &b, id, w.Name, "scratch", "")
	}
	return b.String()
}

// scan appends one section: the read of root/AGENTS.md and the listing under
// root. An empty root is the worker's scratch, which only the worker knows.
func (h *Harness) scan(ctx context.Context, b *strings.Builder, id, name, kind, root string) {
	r, ok := h.Workers.Get(name)
	if !ok {
		fmt.Fprintf(b, "\nworker %s, %s: not connected\n", name, strings.TrimSpace(kind+" "+root))
		return
	}
	if root == "" {
		root = r.Scratch
	}
	path := root + "/AGENTS.md"
	fmt.Fprintf(b, "\nworker %s, %s %s\nread %s\n", name, kind, root, path)
	rec, cut := execute(ctx, r, id, "read", args{Path: path}, nil)
	fmt.Fprintf(b, "%s\n", bytes.TrimRight(finish("read", args{Path: path}, rec, cut).Body, "\n"))
	interp, code := r.Interps[0], listing(r.Interps[0], root)
	fmt.Fprintf(b, "run %s: %s\n", interp, code)
	rec, cut = execute(ctx, r, id, "run", args{Interpreter: interp, Code: code}, nil)
	fmt.Fprintf(b, "%s\n", bytes.TrimRight(finish("run", args{}, rec, cut).Body, "\n"))
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
