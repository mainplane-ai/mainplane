package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// A started harness listens within a second; one that has not after this
// failed, and its log says why.
const startWait = 10 * time.Second

// Conf is the config the installed harness runs from.
var Conf = filepath.Join(Dir, "config.json")

// envProviders are the providers Default takes from the environment, by the
// variables each provider's own tools read.
var envProviders = map[string]Provider{
	"anthropic": {Key: "$ANTHROPIC_API_KEY"},
	"openai":    {Key: "$OPENAI_API_KEY"},
	"gemini":    {Key: "$GEMINI_API_KEY"},
	"bedrock":   {Key: "$AWS_BEARER_TOKEN_BEDROCK", Region: "$AWS_REGION"},
}

// Default is the config install writes when given none: workers and
// connectors reach port 8080 at host, sessions live under Dir, and every
// provider whose key is set in this environment serves.
func Default(host string) (Config, error) {
	c := Config{Admin: "admin", Host: host, HTTP: ":8080", Providers: map[string]Provider{}}
	var vars []string
	for name, p := range envProviders {
		if os.ExpandEnv(p.Key) != "" {
			c.Providers[name] = p
		}
		v := strings.TrimPrefix(p.Key, "$")
		if p.Region != "" {
			v += " with " + strings.TrimPrefix(p.Region, "$")
		}
		vars = append(vars, v)
	}
	if len(c.Providers) == 0 {
		slices.Sort(vars)
		return c, fmt.Errorf("no provider key is set: set one of %s in this shell and install again. Run install without sudo, which drops the environment; it asks for root itself", strings.Join(vars, ", "))
	}
	return c, nil
}

// Expand fills provider values in from this process's environment, which a
// service will not have, refuses a provider the harness does not know, and
// moves a relative admin under Dir.
func (c *Config) Expand() error {
	if !filepath.IsAbs(c.Admin) {
		c.Admin = filepath.Join(Dir, c.Admin)
	}
	for name, p := range c.Providers {
		for _, s := range []*string{&p.Key, &p.URL, &p.Region} {
			if *s != "" && os.ExpandEnv(*s) == "" {
				return fmt.Errorf("provider %s: %s is empty; set it or put the value in the config. sudo drops the environment, and install asks for root itself", name, *s)
			}
			*s = os.ExpandEnv(*s)
		}
		c.Providers[name] = p
	}
	_, err := providers(c.Providers)
	return err
}

// Install makes this machine run the harness at every boot, from a copy of an
// expanded c at Conf. Dir is root's alone: the copy holds the keys.
func Install(c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := prepare(); err != nil {
		return err
	}
	if err := os.WriteFile(Conf, b, 0o600); err != nil {
		return err
	}
	if err := place(); err != nil {
		return err
	}
	if err := start(); err != nil {
		return err
	}
	return answers(c.HTTP)
}

// answers waits for the harness to answer at addr as only it does: 401 to a
// request with no api key. Another program on the port is an error at once.
func answers(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "localhost"
	}
	url := "http://" + net.JoinHostPort(host, port) + "/"
	client := http.Client{Timeout: time.Second}
	for end := time.Now().Add(startWait); ; time.Sleep(200 * time.Millisecond) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				return fmt.Errorf("%s answered %s, which the harness never does: another program has port %s", url, resp.Status, port)
			}
			return nil
		}
		if time.Now().After(end) {
			return fmt.Errorf("the harness service started but did not answer at %s in %s; its log says why", url, startWait)
		}
	}
}

// place copies this binary to bin through a new file and renames. The old one
// moves aside to .old first, since Windows renames a running binary but will
// not replace it. Installing from bin leaves it. The mode is set apart from
// the write, which the installer's umask would narrow.
func place() error {
	exe, err := os.Executable()
	if err != nil || exe == bin {
		return err
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(bin+".new", b, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(bin+".new", 0o755); err != nil {
		return err
	}
	if err := os.Rename(bin, bin+".old"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(bin+".new", bin); err != nil {
		return errors.Join(err, os.Rename(bin+".old", bin))
	}
	return nil
}

// run is one service manager command; its output is the error when it fails.
func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, out)
	}
	return nil
}
