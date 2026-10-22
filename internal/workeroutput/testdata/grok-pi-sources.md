Grok and Pi fixture provenance

These offline fixtures use vendor-documented event formats. They are examples,
not authenticated provider captures.

- Grok 1.0.62 (`@xai-official/grok`): the bundled headless-mode reference,
  section `streaming-json`, embedded in the native binary. The thought,
  tool_call, tool_call_update, text, and usage examples preserve vendor fields.
  Placeholder signature and aggregate usage values use explicit example data.
  `end.usage` repeats totals; only per-response `usage` records count as tokens.
- Pi 1.1.0 (`@earendil-works/pi-coding-agent`): installed `docs/json.md`,
  `docs/message-types.md`, and `dist/modes/print-mode.js`. The session header,
  tool execution records, and message_update follow the documented examples.
  message_end includes a complete assistant message and explicit example usage.
  `dist/main.js` resolves --session to an existing session and fails if missing.
  --session-id is reserved for creation because it creates missing sessions.
  message_end is authoritative; message_update, turn_end, and agent_end must not
  add duplicate text or token counts.

Offline launch flag verification for Grok 1.0.62:
`grok --no-leader --version` succeeds. An unknown control flag fails parsing.
The binary's top-level parser admits `no_leader`; its agent parser separately
admits `--no-leader`. Dyna uses the top-level headless invocation and reserves
this flag on both initial and resumed turns. It never starts `grok agent leader`.
