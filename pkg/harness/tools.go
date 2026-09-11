package harness

import (
	"encoding/json"

	"github.com/mainplane-ai/mainplane/pkg/provider"
)

const defaultSystem = "You are mainplane, an agent"

// systems holds per-model system prompts. Empty until tuning starts.
var systems = map[string]string{}

// System is the system prompt for a model.
func System(model string) string {
	if s, ok := systems[model]; ok {
		return s
	}
	return defaultSystem
}

func schema(s string) json.RawMessage { return json.RawMessage(s) }

// Tools is the tool set a config record names. Names are checked on Start
// and Configure.
func Tools(name string) []provider.Tool { return toolSets[name] }

// toolSets are the named tool lists a config record can name. Every tool takes
// a worker. The set is fixed for the session's life.
var toolSets = map[string][]provider.Tool{
	"default": {
		{Name: "run", Description: "Run code on a worker", Schema: schema(`{"type":"object","properties":{"worker":{"type":"string"},"code":{"type":"string"},"interpreter":{"type":"string"}},"required":["worker","code"]}`)},
		{Name: "read", Description: "Read a file", Schema: schema(`{"type":"object","properties":{"worker":{"type":"string"},"path":{"type":"string"}},"required":["worker","path"]}`)},
		{Name: "write", Description: "Write a file", Schema: schema(`{"type":"object","properties":{"worker":{"type":"string"},"path":{"type":"string"},"content":{"type":"string"}},"required":["worker","path","content"]}`)},
		{Name: "edit", Description: "Edit a file with replacement", Schema: schema(`{"type":"object","properties":{"worker":{"type":"string"},"path":{"type":"string"},"old":{"type":"string"},"new":{"type":"string"}},"required":["worker","path","old","new"]}`)},
	},
}
