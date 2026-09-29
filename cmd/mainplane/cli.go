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
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/harness"
	"github.com/mainplane-ai/mainplane/pkg/pointer"
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
	"new": {0, 0}, "message": {2, -1}, "tail": {1, 2}, "chat": {1, 1}, "retry": {1, 1}, "stop": {1, 1},
	"info": {1, 1}, "sessions": {0, 1}, "workers": {0, 0}, "worker remove": {1, 1}, "providers": {0, 0},
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
		c.call("POST", "/sessions", "application/json", os.Stdin, &out)
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
		c.chat(args[0])
	case "retry":
		var out struct{ Status string }
		c.call("POST", "/sessions/"+args[0]+"/retry", "", nil, &out)
		fmt.Println(out.Status)
	case "stop":
		c.call("POST", "/sessions/"+args[0]+"/stop?via="+via, "", nil, nil)
	case "info":
		c.raw("/sessions/" + args[0])
	case "sessions":
		var infos []harness.Info
		c.call("GET", "/sessions?status="+strings.Join(args, ""), "", nil, &infos)
		for _, i := range infos {
			fmt.Printf("%s  %-11s  %-40s  n=%-5d context=%d/%d  %s\n", i.ID, i.Status, i.Config.Model, i.N, i.ContextUsed, i.Config.ContextLimit, i.Updated.Local().Format(time.DateTime))
		}
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
		}
	case "worker remove":
		c.call("DELETE", "/workers/"+url.PathEscape(args[0]), "", nil, nil)
	case "providers":
		c.raw("/providers")
	}
}

// client is what login wrote: the harness's key, the URL it was last found
// at, and the api key's secret.
type client struct {
	URL     string `json:"url"`
	Key     string `json:"key"`
	Harness string `json:"harness"`
}

// login is what login wrote, with the harness proved at its URL, or found
// again through the pointer and saved.
func login() (client, error) {
	var c client
	b, err := os.ReadFile(loginPath())
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	url, err := pointer.Find(context.Background(), c.Harness, c.URL)
	if err != nil || url == c.URL {
		return c, err
	}
	c.URL = url
	return c, c.save()
}

func (c client) save() error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(loginPath()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(loginPath(), b, 0o600)
}

// call does one request. A status outside 2xx is the harness's own words on
// stderr and exit 1. A JSON reply lands in out when out is given.
func (c client) call(method, path, ctype string, body io.Reader, out any) *http.Response {
	resp, err := c.do(method, path, ctype, body)
	if err != nil {
		die(err)
	}
	if out != nil {
		defer func() { _ = resp.Body.Close() }()
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			die(err)
		}
	}
	return resp
}

// do does one request; a status outside 2xx is an error in the harness's own
// words.
func (c client) do(method, path, ctype string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, c.URL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp, nil
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
	v, err := c.harness()
	if err != nil {
		die(err)
	}
	if !version.Match(v) {
		die(fmt.Errorf("harness is version %s, this mainplane is %s: run mainplane update", v, version.V))
	}
	return v
}

// harness is the release the harness runs.
func (c client) harness() (string, error) {
	resp, err := c.do("GET", "/", "", nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct{ Version string }
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out.Version, err
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
