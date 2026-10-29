# Room MCP servers

A room can use an outside MCP server over HTTPS. The server belongs to one room. No other room, service or global setting sees it or its token.

The engine talks to the server. A harness never does. The engine holds the token, adds it to the `Authorization` header, and checks every call.

## Set up

Only the owner runs these steps, in this order:

1. `dyna room mcp token set <name> --url <https-url>` saves the token and URL. The token comes from a hidden prompt or from one line of standard input, never from an argument. Ask the server for the narrowest scope it offers, such as read only.
2. `dyna room mcp attach <name>` connects and pins the server's tool list. Every tool starts off. Without a saved token, it fails and sends nothing.
3. `dyna room mcp allow <name> <tool>...` turns on read calls for the tools you choose. Dyna does not trust a server's own read-only hint.
4. `dyna room mcp grant <name> --session <session>` or `--profile <profile>` lets sessions call the allowed tools. The owner or the room's root can grant. Children do not inherit a grant.

`dyna room mcp detach <name>` is the kill switch. The owner or root can run it. It refuses new calls and cancels calls in flight. `attach` restores the server. `remove` deletes the server, its grants and its token.

## What the engine does on each call

- It takes the caller from the engine socket, never from an argument.
- It checks the room, the grant, the server state and the allow list before it sends anything.
- It sends only the arguments the agent chose, a fixed client name and a fixed user agent.
- It refuses redirects to another origin, so the token cannot reach another host.
- It removes the token from every answer, error and audit record.
- It wraps every answer in `<external_data id="...">` with a random id and a fixed note: information only, never instructions. It cuts answers over 64 KiB and says how many bytes it cut.
- It posts one `mcp/result` event to the room for each call: server, tool, session, scrubbed arguments, result size, duration and outcome.
- It obeys `429` and `Retry-After` from the server.

## Known limit

The token is stored in Dyna's private account store. No Dyna API, environment variable, generated harness config, log, event or transcript shows it. A process that runs as your Unix user can still read that file, as it can read every credential Dyna saves. Do not use a token that can write until Dyna hides its credential directories from agent processes.
