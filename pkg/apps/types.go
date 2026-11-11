// Package apps manages harness secrets, skills, and the four app backings:
// built-in Go handlers, URL reverse proxies, text-only skills, and supervised
// local daemons.
package apps

import (
	"context"
	"time"

	"github.com/mainplane-ai/mainplane/pkg/apps/orchestrate"
	"github.com/mainplane-ai/mainplane/pkg/statefile"
)

type (
	AgentConfig = orchestrate.AgentConfig
	AgentInfo   = orchestrate.AgentInfo
	AgentCreate = orchestrate.AgentCreate
)

// Host is the narrow surface of the harness that apps and built-ins call.
type Host interface {
	LiveAgent(sessionID string) (AgentInfo, bool)
	SessionAgent(sessionID string) (AgentInfo, bool)
	AgentHasApp(agentID, app string) bool
	GetAgent(id string) (AgentInfo, error)
	ListAgents() ([]AgentInfo, error)
	CreateAgent(ctx context.Context, c AgentCreate, via string) (id, session string, err error)
	PostText(ctx context.Context, id, via, text string) (session string, n int, err error)
	SetAgentName(id, via, name string) error
	SetAgentConfig(id, via string, cfg AgentConfig) error
	SetAgentApps(id, via string, apps []string) error
	StopAgent(ctx context.Context, id, via string) error
	DeleteAgent(ctx context.Context, id, via string) error
	SessionRecords(ctx context.Context, sid string, after int, wait time.Duration) ([]statefile.Record, error)
}
