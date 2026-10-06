package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/auth"
	"github.com/mainplane-ai/mainplane/pkg/mesh"
	"github.com/mainplane-ai/mainplane/pkg/pointer"
	"github.com/mainplane-ai/mainplane/pkg/worker"
)

// A started harness listens within a second; one that has not after this
// failed, and its log says why. Its tunnel takes longer: the first start
// downloads cloudflared, about 50 MB.
const (
	startWait  = 10 * time.Second
	tunnelWait = 3 * time.Minute
)

// Conf is the config the installed harness runs from.
var Conf = filepath.Join(Dir, "config.json")

// adminAgents is admin's root AGENTS.md, which install writes in its scratch
// when none is there: what only the worker on the harness's machine can do.
// Every session with admin reads it whole. Conf, then logs.
const adminAgents = `# Admin:
- This worker is called admin, a reserved name for workers
- The worker is on the machine that runs mainplane-server, the Mainplane harness
- Because you have access to admin and are reading this, you are acting as a Mainplane admin agent
- You have full access to the config of mainplane-server, secrets, rules, and access to all connected workers
- You must be careful and responsible with these permissions
- If the user wants agents without admin access, their sessions should not include the admin worker
- The mainplane CLI here is logged in to this harness with a full API key. mainplane --help lists every verb
- You can change this session's workers: mainplane workers <session id> <name>..., the whole list, admin included
- Harness config: %s. Providers, links and drives apply when it is saved. It holds the provider keys: never print it whole
- Harness logs: %s
- Remember that the mainplane-server and worker are open source and the code can be referenced
`

// envProviders are the providers Default takes from the environment, by the
// variables each provider's own tools read.
var envProviders = map[string]Provider{
	"anthropic": {Key: "$ANTHROPIC_API_KEY"},
	"openai":    {Key: "$OPENAI_API_KEY"},
	"google":    {Key: "$GEMINI_API_KEY"},
	"bedrock":   {Key: "$AWS_BEARER_TOKEN_BEDROCK", Region: "$AWS_REGION"},
}

// Default is the config install writes when given none and none is
// installed: the tunnel carries workers and connectors to port 8080 on
// loopback, and every provider whose key is set in this environment serves.
// With none set, keys go in the config after install.
func Default() Config {
	c := Config{HTTP: "127.0.0.1:8080", Providers: map[string]Provider{}}
	for name, p := range envProviders {
		if os.ExpandEnv(p.Key) != "" {
			c.Providers[name] = p
		}
	}
	return c
}

// Expand fills provider values in from this process's environment, which a
// service will not have, and refuses a provider the harness does not know.
func (c *Config) Expand() error {
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
// expanded c at Conf, or with keep from the config already there, as it is,
// which c becomes, makes it the worker admin while the tunnel starts, and
// returns the harness's URL once it answers there. Dir is root's and the
// operator's alone: the config holds the keys, and admin runs code as the
// operator so that it may read and edit them.
func Install(c *Config, keep bool) (string, error) {
	if err := prepare(); err != nil {
		return "", err
	}
	if _, err := os.Stat(Conf); keep && err == nil {
		if *c, err = Load(Conf); err != nil {
			return "", err
		}
	} else {
		b, err := json.MarshalIndent(c, "", "  ")
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(Conf, b, 0o600); err != nil {
			return "", err
		}
	}
	k, err := Key(Dir)
	if err != nil {
		return "", err
	}
	if err := place(); err != nil {
		return "", err
	}
	if err := start(); err != nil {
		return "", err
	}
	if err := answers(c.HTTP); err != nil {
		return "", err
	}
	if err := admin(pointer.Encode(k)); err != nil {
		return "", err
	}
	if err := agents(); err != nil {
		return "", err
	}
	return reached()
}

// writeAgents writes adminAgents at path unless a file is there, which is
// the user's to edit and stays as it is. It reports whether it wrote.
func writeAgents(path string) (bool, error) {
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	return true, os.WriteFile(path, fmt.Appendf(nil, adminAgents, Conf, logs), 0o644)
}

// admin makes this machine the worker admin of the harness with key harness,
// through the CLI the install line put beside this binary, which the worker
// install places as the machine's only one. A machine already a worker of
// this harness stays as it is. admin's secret is in no token anyone sees, and
// new at each install that joins it. Its output is shown only when it fails.
func admin(harness string) error {
	if h, _, err := mesh.Joined(); err == nil && h == harness {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	secret := rand.Text()
	if err := Auth(Dir).Set(auth.Join, worker.Admin, secret); err != nil {
		return err
	}
	cli := filepath.Join(filepath.Dir(exe), "mainplane"+filepath.Ext(exe))
	if out, err := exec.Command(cli, "install", auth.Token(auth.Join, harness, secret)).CombinedOutput(); err != nil {
		return fmt.Errorf("the admin worker: %w: %s", err, out)
	}
	return nil
}

// uninstallAdmin removes the worker admin, and the CLI with it, which goes
// with the harness it was installed with.
func uninstallAdmin() error {
	if _, err := os.Stat(worker.Bin); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return run(worker.Bin, "uninstall")
}

// run is one command; its output is the error when it fails.
func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, out)
	}
	return nil
}

// SetTunnel makes the installed harness reached through t, or through a quick
// tunnel when t is nil, restarts it, and returns its URL once it answers
// there. Workers follow through the pointer.
func SetTunnel(t *Tunnel) (string, error) {
	c, err := Load(Conf)
	if err != nil {
		return "", fmt.Errorf("no installed harness: %w", err)
	}
	c.Tunnel = t
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(Conf, b, 0o600); err != nil {
		return "", err
	}
	if err := restart(); err != nil {
		return "", err
	}
	return reached()
}

// reached waits for the pointer to name a URL where the harness proves its
// key, through Cloudflare, as a worker finds it, and returns that URL.
func reached() (string, error) {
	k, err := Key(Dir)
	if err != nil {
		return "", err
	}
	for end := time.Now().Add(tunnelWait); ; time.Sleep(time.Second) {
		url, err := pointer.Find(context.Background(), pointer.Encode(k), "")
		if err == nil {
			return url, nil
		}
		if time.Now().After(end) {
			return "", fmt.Errorf("the harness did not answer through its tunnel in %s: %w; its log says why", tunnelWait, err)
		}
	}
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
