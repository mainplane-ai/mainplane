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
	"info": {1, 1}, "name": {1, -1}, "sessions": {0, 1}, "workers": {0, 0}, "workers set": {2, -1}, "worker remove": {1, 1}, "worker rename": {2, 2}, "providers": {0, 0}, "provider": {3, 4}, "key": {2, 2}, "signin": {1, 1},
	"agents": {0, 1}, "agent new": {0, 0}, "agent info": {1, 1}, "agent message": {2, -1}, "agent retry": {1, 1}, "agent stop": {1, 1}, "agent delete": {1, 1}, "agent name": {1, -1}, "agent config": {1, 1}, "agent apps": {1, -1},
	"apps": {0, 0}, "app start": {1, 1}, "app stop": {1, 1}, "secrets": {0, 0}, "secret set": {1, 2}, "secret delete": {1, 1}, "skill": {0, 1}, "app": {3, 4},
}

// methods are what the app verb calls an app with: the standard ones. A
// word that is none of them is a slip, not a method.
var methods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE"}

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
	case "worker rename":
		c.call("PUT", "/workers/"+url.PathEscape(args[0])+"/name", "text/plain", strings.NewReader(args[1]), nil)
	case "providers":
		c.raw("/providers")
	case "provider":
		body, _ := json.Marshal(map[string]string{"shape": args[1], "url": args[2], "key": strings.Join(args[3:], "")})
		c.call("PUT", "/providers/"+url.PathEscape(args[0]), "application/json", bytes.NewReader(body), nil)
	case "key":
		c.key(args[0], args[1])
	case "signin":
		c.signin(args[0])
	default:
		c.agent(verb, args)
	}
}

// agent is the verbs on agents, apps and secrets. Each route is the
// connector's to name; a body from stdin is read here, so one that is not
// the JSON the route takes ends the verb before the harness is asked.
func (c client) agent(verb string, args []string) {
	ctx := context.Background()
	var err error
	switch verb {
	case "agents":
		infos, err := c.Agents(ctx, "", strings.Join(args, ""))
		if err != nil {
			die(err)
		}
		for _, i := range infos {
			fmt.Printf("%s  %-11s  %-40s  session=%s  context=%d/%d  %s  %-14s  %s\n", i.ID, i.Status, i.Config.Model, i.CurrentSession, i.ContextUsed, i.Config.ContextLimit, i.Updated.Local().Format(time.DateTime), i.Via, i.Name)
		}
	case "agent new":
		var cr harness.AgentCreate
		if err := json.NewDecoder(os.Stdin).Decode(&cr); err != nil {
			die(err)
		}
		id, session, err := c.CreateAgent(ctx, cr, via)
		if err != nil {
			die(err)
		}
		fmt.Println(id, session)
	case "agent info":
		c.raw("/agents/" + args[0]) // printed as it came, as info prints a session
	case "agent message":
		s, n, err := c.PostAgent(ctx, args[0], via, args[1], args[2:])
		if err != nil {
			die(err)
		}
		fmt.Println(s, n)
	case "agent retry":
		st, err := c.RetryAgent(ctx, args[0])
		if err != nil {
			die(err)
		}
		fmt.Println(st)
	case "agent stop":
		err = c.StopAgent(ctx, args[0], via)
	case "agent delete":
		err = c.DeleteAgent(ctx, args[0], via)
	case "agent name":
		err = c.SetAgentName(ctx, args[0], via, strings.Join(args[1:], " "))
	case "agent config":
		var conf harness.AgentConfig
		if err = json.NewDecoder(os.Stdin).Decode(&conf); err == nil {
			err = c.SetAgentConfig(ctx, args[0], via, conf)
		}
	case "agent apps":
		err = c.SetAgentApps(ctx, args[0], via, args[1:])
	case "apps":
		infos, err := c.Apps(ctx)
		if err != nil {
			die(err)
		}
		for _, i := range infos {
			fmt.Printf("%-20s  %-8s  %-15s  %s\n", i.Name, i.Kind, strings.TrimSpace(i.Status+" "+strings.Join(i.MissingSecrets, ",")), i.Description)
		}
	case "app start", "app stop":
		err = c.RunApp(ctx, args[0], verb == "app start")
	case "secrets":
		names, err := c.Secrets(ctx)
		if err != nil {
			die(err)
		}
		for _, n := range names {
			fmt.Println(n)
		}
	case "secret set":
		err = c.SetSecret(ctx, args[0], secretValue(args[1:]))
	case "secret delete":
		err = c.DeleteSecret(ctx, args[0])
	}
	if err != nil {
		die(err)
	}
}

// secretValue is the value secret set was given: its arguments, or with none
// stdin, where a value stays out of the shell's history and may be many
// lines. An empty one would count as there, and start its apps with nothing.
func secretValue(args []string) string {
	value := strings.Join(args, "")
	if len(args) == 0 {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			die(err)
		}
		value = strings.TrimRight(string(b), "\r\n")
	}
	if strings.TrimSpace(value) == "" {
		die(errors.New("a secret has a value: mainplane secret delete <name> removes one"))
	}
	return value
}

// skill is the two verbs an agent uses: with one argument or none, read a
// skill or list them; with more, call the app and print its reply as it came.
// In an agent's session on a worker the environment is the login: the
// session's key, which reaches the agent's skills and apps and no more.
func skill(verb string, args []string) {
	if a := arity[verb]; len(args) < a[0] || len(args) > a[1] {
		usage()
	}
	// The method is checked before the harness is asked or even found:
	// "app stop x y" is a slip for the stop verb, not a call of an app named stop.
	if verb == "app" && !slices.Contains(methods, strings.ToUpper(args[1])) {
		usage()
	}
	s, ok := connector.Session()
	if !ok {
		c, err := login()
		if err != nil {
			die(err)
		}
		s = c.Client
	}
	c := client{s}
	ctx := context.Background()
	switch len(args) {
	case 0:
		skills, err := c.Skills(ctx)
		if err != nil {
			die(err)
		}
		for _, s := range skills {
			fmt.Printf("%-20s  %s\n", s.Name, s.Description)
		}
	case 1:
		text, err := c.Skill(ctx, args[0])
		if err != nil {
			die(err)
		}
		fmt.Print(text)
	default:
		var body io.Reader
		ctype := ""
		if len(args) == 4 {
			body, ctype = strings.NewReader(args[3]), "application/json"
			if args[3] == "-" {
				body = os.Stdin
			}
		}
		resp, err := c.App(ctx, args[0], strings.ToUpper(args[1]), args[2], ctype, body)
		if err != nil {
			die(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if _, err := io.Copy(os.Stdout, resp.Body); err != nil {
			die(err)
		}
	}
}

// key sets a provider's key, and that of each catalog provider that takes
// the same key. A provider the harness has keeps its shape, url and session;
// a new one gets the catalog's.
func (c client) key(name, key string) {
	if strings.TrimSpace(key) == "" { // sent with the catalog's url, it would make a provider with no key
		die(errors.New("a key is one line"))
	}
	var have []harness.ProviderInfo
	c.call("GET", "/providers", "", nil, &have)
	cat, err := catalog.Fetch(context.Background())
	if err != nil {
		die(err)
	}
	for _, k := range cat.Keyed(name) {
		p := cat.Providers[k]
		if slices.ContainsFunc(have, func(h harness.ProviderInfo) bool { return h.Name == k }) {
			p = catalog.Provider{}
		}
		body, _ := json.Marshal(map[string]string{"shape": p.Shape, "url": p.URL, "session": p.Session, "key": key})
		c.call("PUT", "/providers/"+url.PathEscape(k), "application/json", bytes.NewReader(body), nil)
	}
}

// catalogProvider is the shape and url to send for provider name: none for a
// provider the harness has, which keeps its own, the catalog's for a new one.
func (c client) catalogProvider(name string) catalog.Provider {
	var have []harness.ProviderInfo
	c.call("GET", "/providers", "", nil, &have)
	if slices.ContainsFunc(have, func(h harness.ProviderInfo) bool { return h.Name == name }) {
		return catalog.Provider{}
	}
	cat, err := catalog.Fetch(context.Background())
	if err != nil {
		die(err)
	}
	return cat.Providers[name]
}

// signin runs a provider's login on the harness: it prints the prompt, sends
// a line of stdin, if one comes, when the prompt asks for a paste, and waits
// for the end.
func (c client) signin(name string) {
	ctx := context.Background()
	p := c.catalogProvider(name)
	st, err := c.SignIn(ctx, name, p.Shape, p.URL)
	if err != nil {
		die(err)
	}
	fmt.Printf("%s\n\n%s\n", st.Text, st.URL)
	if st.Code != "" {
		fmt.Printf("\ncode: %s\n", st.Code)
	}
	if st.Paste {
		fmt.Print("\npaste: ")
		go func() { // the login can end without it: the browser may reach the callback itself
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if strings.TrimSpace(line) == "" {
				return
			}
			if err := c.SignInPaste(ctx, name, strings.TrimSpace(line)); err != nil {
				die(err)
			}
		}()
	}
	if err := c.SignInWait(ctx, name); err != nil {
		die(err)
	}
	fmt.Println("signed in: " + name)
}

// client is the harness API with every failure the harness's own words on
// stderr and exit 1.
type client struct{ *connector.Client }

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
