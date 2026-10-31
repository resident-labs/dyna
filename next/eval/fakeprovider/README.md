# Scripted upstream for surface evaluation

This evaluation-only server is an OpenAI chat-completions streaming upstream.
It does not run Pi or CLIProxyAPI. Run both separately in an isolated sandbox.
Never use the trial proxy configuration, credentials, or data directory.
Restrict sandbox network access to loopback.

Build the command on the sandbox host and start it with a fixture file:

```sh
go build -o "$SCRATCH/fakeprovider" ./eval/fakeprovider
"$SCRATCH/fakeprovider" -fixture "$SCRATCH/fixture.json" -listen 127.0.0.1:0 \
  > "$SCRATCH/requests.jsonl" 2> "$SCRATCH/listen.txt" &
fixture_pid=$!
```

The first line of `listen.txt` is the chosen loopback address. Configure a
separate CLIProxyAPI instance with an OpenAI-compatible provider whose base URL
is `http://<address>/v1`, a dummy upstream key, and model name and alias equal
to the fixture model. Use a separate proxy client key and empty auth directory.
Run Pi against that proxy using the fixture model and a dummy key. A full-engine
run additionally needs a sandbox-only way to configure its supervised proxy;
this tool does not provide one.

Example fixture:

```json
{"models":{"case-1":[
  {"id":"call","calls":[{"id":"call-1","name":"post","arguments":{"text":"hello"}}],"input_tokens":100,"output_tokens":10},
  {"id":"finish","text":"done","input_tokens":110,"output_tokens":5}
]}}
```

Each request consumes the next step for its model. An unknown or exhausted
model returns HTTP 400. Use a distinct model per isolated session; sequential
requests within a model preserve script order. Scripted usage is synthetic.
This fixture verifies plumbing, not model accuracy or provider behavior.

The server writes one JSON line per accepted request to stdout. It contains
only the fixture model, step ID, per-model request count, and `tools_sha256`.
The hash covers the exact serialized upstream `tools` value; missing tools
hashes zero bytes, distinct from `[]`. Compare these hashes with the proxy
hook in request order. The fixture cannot identify compaction from a translated
request: correlate the session and request ordinal with the runner's Pi records.
Mark an ambiguous compaction boundary unmeasured; do not infer it.
No request bodies or headers are recorded.

Stop Pi and the separate proxy, then `kill "$fixture_pid"` and wait for it.
Remove the sandbox directory after collecting the permitted records.
`127.0.0.1` and other literal loopback IPs are the only allowed listen hosts.
