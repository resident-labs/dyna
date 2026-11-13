# Voice

The prompt and RealtimeV2 session/tool definitions follow OpenAI Codex at
`6242064da0412b4c12176a2e53ef91afb333002d` (Apache-2.0):

- `codex-rs/prompts/templates/realtime/backend_prompt.md`
- `codex-rs/codex-api/src/endpoint/realtime_websocket/methods_v2.rs`
- `codex-rs/codex-api/src/endpoint/realtime_websocket/protocol_v2.rs`
- `codex-rs/core/src/realtime_conversation.rs`

The prompt only renames Codex to Dyna and removes the user-name paragraph.
The provider schedules responses and interrupts them. The engine does not retry,
queue spoken responses, or batch posts. Dyna's backend is the room root's owner DM.

## Live transport probe

This test spends account quota. From `next/` on the engine host:

```sh
DYNA_VOICE_LIVE=1 \
DYNA_VOICE_PROXY_SOCKET=/path/to/private/proxy/proxy.sock \
DYNA_VOICE_PROXY_CONFIG=/path/to/private/proxy/config.yaml \
DYNA_VOICE_AUTH_IDS='eligible-chatgpt-auth-id' \
go test ./engine/voice -run '^TestLiveProxyAudio$' -v -count=1
```

Use the engine's private vendored proxy socket, **not** the owner-facing model
forwarder's socket. The config is the private proxy's generated config; the test
reads its API key without logging it. Auth IDs are proxy auth IDs (not room
account display names), comma separated. Supply only accounts eligible for the
room. The test sends the production session update and a text request, and must
receive nonempty PCM audio within 45 seconds. It skips unless explicitly enabled.

Offline tests: `go test -race ./engine/voice`.
