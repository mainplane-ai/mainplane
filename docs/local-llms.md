# Local LLMs

```
 harness --mesh--> http://gpu-box:11434/v1 --> model server --> GPU
```

A model you run yourself is a provider like any other. Any server that speaks
OpenAI Chat Completions works: Ollama, LM Studio, the llama.cpp server, vLLM.
The harness needs its base url and nothing else. The steps below use Ollama;
only step 1 differs for another server.

## Setup

1. Serve the model where the harness can reach it. On the harness's own
   machine, the default `http://localhost:11434/v1` is enough. On another
   machine, make that machine a worker: the harness then reaches it by its
   worker name over the mesh, and no port opens to the internet. The mesh is
   IPv6, so the server must listen on every address, not on loopback alone:

   ```
   OLLAMA_HOST=0.0.0.0 OLLAMA_CONTEXT_LENGTH=32768 ollama serve
   ollama pull <model>
   ```

2. Add the provider. Its name is free, its shape is `openai-chat`, its url is
   the server's base url. A local server takes no key:

   ```
   mainplane provider ollama openai-chat http://gpu-box:11434/v1
   ```

   or in the harness config:

   ```
   "providers": {"ollama": {"shape": "openai-chat", "url": "http://gpu-box:11434/v1"}}
   ```

3. Start a session. The catalog has no local models, so the terminal app does
   not list them: make the session from the CLI, then open it. The model is
   `<provider>/<the server's model name>`, and the context limit is yours to
   set:

   ```
   echo '{"model":"ollama/<model>","context_limit":30000,"workers":[{"name":"gpu-box"}]}' | mainplane new
   mainplane <session id>
   ```

## The context window

A local server loads a model with a fixed context window, often far smaller
than the model's own. The system prompt and the tools take about 4,000 tokens
before the first message, and each root AGENTS.md adds to that.

- Set the server's window on the server: `OLLAMA_CONTEXT_LENGTH` for Ollama,
  `--ctx-size` for llama.cpp, `--max-model-len` for vLLM. 32,000 tokens is a
  workable floor.
- Set the session's `context_limit` under the server's window. The harness
  then ends the session in order before the server's window is full.
- Past its window, Ollama's Chat Completions endpoint drops the start of the
  prompt and answers as if nothing happened. The model loses the system prompt
  and the tools, and calls tools that do not exist. The harness fails the step
  when it sees a prompt shrink between two steps of one turn. It cannot see a
  first prompt that is already too large.
- A stop of `length` after a few output tokens means the window is full, not
  the output limit.

## The model

The model must support tool calls: every action of a Mainplane agent is one.
Small models call tools poorly; they name workers that are not in the session
or stop mid-task. Pick the largest model with tool support that fits the GPU
with the window above.
