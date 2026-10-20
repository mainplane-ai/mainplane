package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mainplane-ai/mainplane/pkg/catalog"
	"github.com/mainplane-ai/mainplane/pkg/connector"
	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
	"github.com/mainplane-ai/mainplane/pkg/version"
)

// via is what this connector writes on every record it causes.
const via = "cli"

// resultLines is how much of a result chat and tail show before the count.
const resultLines = 20

// arity is how many arguments each verb takes, least and most; -1 is any
// number.
var arity = map[string][2]int{
	"new": {0, 0}, "message": {2, -1}, "tail": {1, 2}, "chat": {0, 1}, "retry": {1, 1}, "stop": {1, 1}, "delete": {1, 1},
	"info": {1, 1}, "name": {1, -1}, "sessions": {0, 1}, "workers": {0, 0}, "workers set": {2, -1}, "worker remove": {1, 1}, "providers": {0, 0}, "key": {2, 2},
}

// cli is the harness API as verbs: one verb, one route, its reply printed.
// The harness and the key come from login; nothing else is remembered or
// defaulted, and a reply the harness refuses is printed as it came and exits
// 1. The command line is checked whole before the harness is asked anything.
func cli(verb string, args []string) {
	a, ok := arity[verb]
	if !ok || len(args) < a[0] || a[1] >= 0 && len(args) > a[1] {
		usage()
	}
	c, err := login()
	if errors.Is(err, fs.ErrNotExist) {
		die(fmt.Errorf("not logged in: mainplane login <api key>"))
	}
	if err != nil {
		die(err)
	}
	hv := c.version()
	switch verb {
	case "new":
		var out struct{ ID string }
		c.call("POST", "/sessions?via="+via, "application/json", os.Stdin, &out)
		fmt.Println(out.ID)
	case "message":
		fmt.Println(c.post(args[0], args[1], args[2:]))
	case "tail":
		after := 0
		if len(args) == 2 {
			var err error
			if after, err = strconv.Atoi(args[1]); err != nil {
				usage()
			}
		}
		for _, r := range c.records(args[0], after, "") {
			render(r)
		}
	case "chat":
		c.chat(strings.Join(args, ""))
	case "retry":
		var out struct{ Status string }
		c.call("POST", "/sessions/"+args[0]+"/retry", "", nil, &out)
		fmt.Println(out.Status)
	case "stop":
		c.call("POST", "/sessions/"+args[0]+"/stop?via="+via, "", nil, nil)
	case "delete":
		if err := c.Delete(context.Background(), args[0], via); err != nil {
			die(err)
		}
	case "info":
		c.raw("/sessions/" + args[0])
	case "sessions":
		var infos []harness.Info
		c.call("GET", "/sessions?status="+strings.Join(args, ""), "", nil, &infos)
		for _, i := range infos {
			fmt.Printf("%s  %-11s  %-40s  n=%-5d context=%d/%d  %s  %s\n", i.ID, i.Status, i.Config.Model, i.N, i.ContextUsed, i.Config.ContextLimit, i.Updated.Local().Format(time.DateTime), i.Name)
		}
	case "name":
		var out struct{ N int }
		c.call("PUT", "/sessions/"+args[0]+"/name?via="+via, "text/plain", strings.NewReader(strings.Join(args[1:], " ")), &out)
		fmt.Println(out.N)
	case "workers set":
		ws := make([]statefile.Worker, 0, len(args)-1)
		for _, name := range args[1:] {
			ws = append(ws, statefile.Worker{Name: name})
		}
		b, _ := json.Marshal(ws) // names are strings: it cannot fail
		var out struct{ N int }
		c.call("PUT", "/sessions/"+args[0]+"/workers?via="+via, "application/json", bytes.NewReader(b), &out)
		fmt.Println(out.N)
	case "workers":
		var ws []harness.Listed
		c.call("GET", "/workers", "", nil, &ws)
		for _, w := range ws {
			state := "connected"
			if w.Refused != "" {
				state = "refused: " + w.Refused
			}
			if w.Version != hv { // a worker follows its harness; say so only when it has not
				state += " at " + w.Version
			}
			fmt.Printf("%-28s  %-13s  %-10s  %s\n", w.Name, w.OS+"/"+w.Arch, strings.Join(w.Interps, ","), state)
			for _, d := range w.Drives {
				fmt.Printf("  drive %v\n", d)
			}
		}
	case "worker remove":
		c.call("DELETE", "/workers/"+url.PathEscape(args[0]), "", nil, nil)
	case "providers":
		c.raw("/providers")
	case "key":
		c.key(args[0], args[1])
	}
}

// key sets a provider's key. A provider the harness has keeps its shape and
// url; a new one gets the catalog's.
func (c client) key(name, key string) {
	var have []harness.ProviderInfo
	c.call("GET", "/providers", "", nil, &have)
	p := catalog.Provider{}
	if !slices.ContainsFunc(have, func(h harness.ProviderInfo) bool { return h.Name == name }) {
		cat, err := catalog.Fetch(context.Background())
		if err != nil {
			die(err)
		}
		p = cat.Providers[name]
	}
	body, _ := json.Marshal(map[string]string{"shape": p.Shape, "url": p.URL, "key": key})
	c.call("PUT", "/providers/"+url.PathEscape(name), "application/json", bytes.NewReader(body), nil)
}

// client is the harness API with every failure the harness's own words on
// stderr and exit 1.
type client struct{ connector.Client }

func login() (client, error) {
	c, err := connector.Load(context.Background())
	return client{c}, err
}

// call does one request. A JSON reply lands in out when out is given.
func (c client) call(method, path, ctype string, body io.Reader, out any) {
	if err := c.Call(context.Background(), method, path, ctype, body, out); err != nil {
		die(err)
	}
}

// raw prints a JSON reply as the harness sent it, indented.
func (c client) raw(path string) {
	var v json.RawMessage
	c.call("GET", path, "", nil, &v)
	var b bytes.Buffer
	if err := json.Indent(&b, v, "", "  "); err != nil {
		die(err)
	}
	fmt.Println(b.String())
}

// version refuses a harness from another release, and returns the one it runs.
func (c client) version() string {
	v, err := c.Version(context.Background())
	if err != nil {
		die(err)
	}
	if !version.Match(v) {
		die(fmt.Errorf("harness is version %s, this mainplane is %s: run mainplane login <api key> to become its release", v, version.V))
	}
	return v
}

// post appends text and files as one turn. Returns the last record's n.
func (c client) post(id, text string, files []string) int {
	n, err := c.Post(context.Background(), id, via, text, files)
	if err != nil {
		die(err)
	}
	return n
}

// records is the file after record n; with wait, a long poll.
func (c client) records(id string, after int, wait string) []statefile.Record {
	recs, err := c.Records(context.Background(), id, after, wait)
	if err != nil {
		die(err)
	}
	return recs
} // chat is two loops and nothing between them. One long polls the records and

// renders each as it lands, printing a prompt when the file says the session
// is closed or failed. The other posts every stdin line as a message. Your
// own line comes back as a message record: that is the receipt, and it shows
// where it landed, behind a running step if there was one. Ctrl+C prints the
// id and touches nothing on the harness.
// With no id, it follows the session updated last.
func (c client) chat(id string) {
	if id == "" {
		var infos []harness.Info
		c.call("GET", "/sessions?limit=1", "", nil, &infos)
		if len(infos) == 0 {
			die(errors.New("no sessions: mainplane new"))
		}
		id = infos[0].ID
	}
	fmt.Println(id)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt)
	go func() {
		<-quit
		fmt.Println("\n" + id)
		os.Exit(0)
	}()
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 64<<10), harness.MaxPost)
		for sc.Scan() {
			if line := sc.Text(); line != "" {
				c.post(id, line, nil)
			}
		}
		if err := sc.Err(); err != nil {
			die(err)
		}
		quit <- os.Interrupt
	}()
	var chain []statefile.Record
	for {
		recs := c.records(id, len(chain), "30s")
		for _, r := range recs {
			render(r)
		}
		chain = append(chain, recs...)
		if st := statefile.Derive(chain); len(recs) > 0 && (st == statefile.StatusClosed || st == statefile.StatusFailed) {
			fmt.Print("> ")
		}
	}
}

// render prints one record: number and kind, the header fields that matter
// for its kind, then the body whole for what a person reads and a line for
// what a person skims. Control bytes print as dots: the terminal answers some
// escape sequences on stdin, and chat would post the answer as a message.
func render(r statefile.Record) {
	head := fmt.Sprintf("%5d  %-8s", r.N, r.Kind)
	n := len(r.Body)
	r.Body = []byte(printable(string(r.Body)))
	r.Via = printable(r.Via)
	switch r.Kind {
	case statefile.Start:
		fmt.Printf("%s  %s\n", head, r.Time.Local().Format(time.DateTime))
	case statefile.Config:
		if r.Via != "" {
			head += "  via=" + r.Via
		}
		fmt.Printf("%s  %s\n", head, r.Body)
	case statefile.System:
		fmt.Printf("%s  %s | %s\n", head, r.Source, skim(r.Body))
	case statefile.Name:
		fmt.Printf("%s  via=%s  %s\n", head, r.Via, r.Body)
	case statefile.Message:
		if strings.HasPrefix(r.Type, "text/") {
			fmt.Printf("%s  via=%s\n%s\n", head, r.Via, r.Body)
		} else {
			fmt.Printf("%s  via=%s  %s %d bytes\n", head, r.Via, r.Type, n)
		}
	case statefile.Text:
		fmt.Printf("%s\n%s\n", strings.TrimRight(head, " "), r.Body)
	case statefile.Thinking:
		fmt.Printf("%s  %d bytes\n", head, n)
	case statefile.Call:
		var c struct {
			Name string
			Args struct{ Worker, Code, Path string } `json:"arguments"`
		}
		_ = json.Unmarshal(r.Body, &c)
		fmt.Printf("%s  %s %s | %s\n", head, c.Name, c.Args.Worker, printable(skim([]byte(c.Args.Code+c.Args.Path))))
	case statefile.Step:
		fmt.Printf("%s  %s", head, r.Model)
		if r.Usage != nil {
			fmt.Printf("  in=%d out=%d", r.Usage.Input+r.Usage.CacheRead+r.Usage.CacheWrite, r.Usage.Output)
		}
		fmt.Printf("  stop=%s\n", r.Stop)
	case statefile.Result:
		fmt.Printf("%s  for=%s", head, r.For)
		if r.Exit != nil {
			fmt.Printf("  exit=%d", *r.Exit)
		}
		if !strings.HasPrefix(r.Type, "text/") {
			fmt.Printf("  %s %d bytes\n", r.Type, n)
			break
		}
		lines := strings.Split(strings.TrimRight(string(r.Body), "\n"), "\n")
		if len(lines) > resultLines {
			fmt.Printf("\n%s\n  ... %d more lines", strings.Join(lines[:resultLines], "\n"), len(lines)-resultLines)
		} else {
			fmt.Printf("\n%s", strings.Join(lines, "\n"))
		}
		fmt.Println()
	case statefile.Error:
		fmt.Printf("%s  via=%s\n%s\n", head, r.Via, r.Body)
	}
}

// skim is the first line of a body and how much follows it.
func skim(b []byte) string {
	first, rest, more := strings.Cut(strings.TrimSpace(string(b)), "\n")
	if !more {
		return first
	}
	return fmt.Sprintf("%s  (+%d lines)", first, strings.Count(rest, "\n")+1)
}

// printable replaces control characters other than line breaks and tabs with
// dots, and invalid UTF-8 with U+FFFD, so no escape sequence reaches the
// terminal.
func printable(s string) string {
	return strings.Map(func(c rune) rune {
		if unicode.IsControl(c) && c != '\n' && c != '\r' && c != '\t' {
			return '.'
		}
		return c
	}, s)
}

func die(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
