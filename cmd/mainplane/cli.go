package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

// via is what this connector writes on every record it causes.
const via = "cli"

// resultLines is how much of a result chat and tail show before the count.
const resultLines = 20

// cli is the harness API as verbs: one verb, one route, its reply printed.
// Nothing is remembered between runs, nothing is defaulted, and a reply the
// harness refuses is printed as it came and exits 1.
func cli(verb string, args []string) {
	if len(args) == 0 || !strings.HasPrefix(args[0], "http://") && !strings.HasPrefix(args[0], "https://") {
		usage()
	}
	c := client{strings.TrimSuffix(args[0], "/")}
	c.version()
	args = args[1:]
	want := func(n int) {
		if len(args) != n {
			usage()
		}
	}
	switch verb {
	case "new":
		want(1)
		var out struct{ ID string }
		c.call("POST", "/sessions", "application/json", strings.NewReader(args[0]), &out)
		fmt.Println(out.ID)
	case "message":
		if len(args) < 2 {
			usage()
		}
		fmt.Println(c.post(args[0], args[1], args[2:]))
	case "tail":
		if len(args) != 1 && len(args) != 2 {
			usage()
		}
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
		want(1)
		c.chat(args[0])
	case "run":
		want(1)
		var out struct{ Status string }
		c.call("POST", "/sessions/"+args[0]+"/run", "", nil, &out)
		fmt.Println(out.Status)
	case "stop":
		want(1)
		c.call("POST", "/sessions/"+args[0]+"/stop?via="+via, "", nil, nil)
	case "info":
		want(1)
		c.raw("/sessions/" + args[0])
	case "sessions":
		if len(args) > 1 {
			usage()
		}
		var infos []harness.Info
		c.call("GET", "/sessions?status="+strings.Join(args, ""), "", nil, &infos)
		for _, i := range infos {
			fmt.Printf("%s  %-11s  %-40s  n=%-5d prompt=%d/%d  %s\n", i.ID, i.Status, i.Config.Model, i.N, i.Prompt, i.Config.Context, i.Updated.Local().Format(time.DateTime))
		}
	case "workers":
		want(0)
		c.raw("/workers")
	case "providers":
		want(0)
		c.raw("/providers")
	default:
		usage()
	}
}

type client struct{ url string }

// call does one request. A status outside 2xx is the harness's own words on
// stderr and exit 1. A JSON reply lands in out when out is given.
func (c client) call(method, path, ctype string, body io.Reader, out any) *http.Response {
	req, err := http.NewRequest(method, c.url+path, body)
	if err != nil {
		die(err)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		die(err)
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		die(fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(b))))
	}
	if out != nil {
		defer func() { _ = resp.Body.Close() }()
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			die(err)
		}
	}
	return resp
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

// version refuses a harness this binary was not built with.
func (c client) version() {
	var out struct{ Version string }
	c.call("GET", "/", "", nil, &out)
	if out.Version != harness.Version {
		die(fmt.Errorf("harness is version %s, this mainplane is %s", out.Version, harness.Version))
	}
}

// post appends text and files as one turn: a single text/plain body, or one
// multipart part per piece, each typed by its extension. A file with no type
// is refused, not guessed. Returns the last record's n.
func (c client) post(id, text string, files []string) int {
	var out struct{ N int }
	if len(files) == 0 {
		c.call("POST", "/sessions/"+id+"/records?via="+via, "text/plain", strings.NewReader(text), &out)
		return out.N
	}
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	w, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain"}})
	if err != nil {
		die(err)
	}
	_, _ = w.Write([]byte(text))
	for _, f := range files {
		t := mime.TypeByExtension(filepath.Ext(f))
		if t == "" {
			die(fmt.Errorf("%s: no content type for %q", f, filepath.Ext(f)))
		}
		data, err := os.ReadFile(f)
		if err != nil {
			die(err)
		}
		w, err := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {t}})
		if err != nil {
			die(err)
		}
		_, _ = w.Write(data)
	}
	_ = mw.Close()
	c.call("POST", "/sessions/"+id+"/records?via="+via, mw.FormDataContentType(), &b, &out)
	return out.N
}

// records is the file after record n; with wait, a long poll.
func (c client) records(id string, after int, wait string) []statefile.Record {
	resp := c.call("GET", "/sessions/"+id+"/records?after="+strconv.Itoa(after)+"&wait="+wait, "", nil, nil)
	defer func() { _ = resp.Body.Close() }()
	br := bufio.NewReader(resp.Body)
	var recs []statefile.Record
	for {
		r, err := statefile.Decode(br)
		if errors.Is(err, io.EOF) {
			return recs
		}
		if err != nil {
			die(err)
		}
		recs = append(recs, r)
	}
}

// chat is two loops and nothing between them. One long polls the records and
// renders each as it lands, printing a prompt when the file says the session
// is closed or failed. The other posts every stdin line as a message. Your
// own line comes back as a message record: that is the receipt, and it shows
// where it landed, behind a running step if there was one. Ctrl+C prints the
// id and touches nothing on the harness.
func (c client) chat(id string) {
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
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			if line := sc.Text(); line != "" {
				c.post(id, line, nil)
			}
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
// what a person skims.
func render(r statefile.Record) {
	head := fmt.Sprintf("%5d  %-8s", r.N, r.Kind)
	switch r.Kind {
	case statefile.Start:
		fmt.Printf("%s  %s\n", head, r.Time.Local().Format(time.DateTime))
	case statefile.Config:
		fmt.Printf("%s  %s\n", head, r.Body)
	case statefile.System:
		fmt.Printf("%s  %s | %s\n", head, r.Source, skim(r.Body))
	case statefile.Message:
		if strings.HasPrefix(r.Type, "text/") {
			fmt.Printf("%s  via=%s\n%s\n", head, r.Via, r.Body)
		} else {
			fmt.Printf("%s  via=%s  %s %d bytes\n", head, r.Via, r.Type, len(r.Body))
		}
	case statefile.Text:
		fmt.Printf("%s\n%s\n", strings.TrimRight(head, " "), r.Body)
	case statefile.Thinking:
		fmt.Printf("%s  %d bytes\n", head, len(r.Body))
	case statefile.Call:
		var c struct {
			Name string
			Args struct{ Worker, Code, Path string } `json:"arguments"`
		}
		_ = json.Unmarshal(r.Body, &c)
		fmt.Printf("%s  %s %s | %s\n", head, c.Name, c.Args.Worker, skim([]byte(c.Args.Code+c.Args.Path)))
	case statefile.Step:
		fmt.Printf("%s  %s", head, r.Model)
		if r.Usage != nil {
			fmt.Printf("  in=%d out=%d", r.Usage.Input+r.Usage.CacheRead+r.Usage.CacheWrite, r.Usage.Output)
		}
		fmt.Println()
	case statefile.Result:
		fmt.Printf("%s  for=%s", head, r.For)
		if r.Exit != nil {
			fmt.Printf("  exit=%d", *r.Exit)
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

func die(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
