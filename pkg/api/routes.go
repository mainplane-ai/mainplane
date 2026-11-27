package api

import (
	"fmt"
	"net/url"
	"strings"
)

// The paths of the HTTP API. A server registers a method and one of these;
// a client fills its {wildcards} with Path. What each route takes and gives
// is written beside its handler.
const (
	RootPath     = "/"
	JoinPath     = "/join"
	KeysPath     = "/keys"
	KeyPath      = "/keys/{name}"
	DrivesPath   = "/drives"
	DrivePath    = "/drives/{name}"
	SettingsPath = "/settings"

	SessionsPath       = "/sessions"
	SessionPath        = "/sessions/{id}"
	SessionRecordsPath = "/sessions/{id}/records"
	SessionRetryPath   = "/sessions/{id}/retry"
	SessionStopPath    = "/sessions/{id}/stop"
	SessionWorkersPath = "/sessions/{id}/workers"
	SessionNamePath    = "/sessions/{id}/name"

	AgentsPath            = "/agents"
	AgentPath             = "/agents/{id}"
	AgentRecordsPath      = "/agents/{id}/records"
	AgentRetryPath        = "/agents/{id}/retry"
	AgentStopPath         = "/agents/{id}/stop"
	AgentNamePath         = "/agents/{id}/name"
	AgentConfigPath       = "/agents/{id}/config"
	AgentAppsPath         = "/agents/{id}/apps"
	AgentInstructionsPath = "/agents/{id}/instructions"

	WorkersPath    = "/workers"
	WorkerPath     = "/workers/{name}"
	WorkerNamePath = "/workers/{name}/name"
	JoinTokensPath = "/workers/tokens"
	JoinTokenPath  = "/workers/tokens/{name}"

	ProvidersPath         = "/providers"
	ProviderPath          = "/providers/{name}"
	ProviderModelsPath    = "/providers/{name}/models"
	ProviderLoginPath     = "/providers/{name}/login"
	ProviderLoginCodePath = "/providers/{name}/login/code"

	SkillsPath   = "/skills"
	SkillPath    = "/skills/{app}"
	AppsPath     = "/apps"
	AppPath      = "/apps/{name}"
	AppStartPath = "/apps/{name}/start"
	AppStopPath  = "/apps/{name}/stop"
	SecretsPath  = "/secrets"
	SecretPath   = "/secrets/{name}"
)

// Path is path p with each {wildcard} filled by the next of args, escaped,
// and a dot or two as %2E, so no arg is a segment a server cleans away. A
// count of args that does not match is a bug in the caller.
func Path(p string, args ...string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		if strings.HasPrefix(s, "{") {
			if len(args) == 0 {
				panic(fmt.Sprintf("api.Path %s: too few args", p))
			}
			parts[i], args = url.PathEscape(args[0]), args[1:]
			if strings.Trim(parts[i], ".") == "" {
				parts[i] = strings.ReplaceAll(parts[i], ".", "%2E")
			}
		}
	}
	if len(args) > 0 {
		panic(fmt.Sprintf("api.Path %s: too many args", p))
	}
	return strings.Join(parts, "/")
}
