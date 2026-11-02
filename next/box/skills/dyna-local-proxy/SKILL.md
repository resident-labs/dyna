---
name: dyna-local-proxy
description: Use when the owner wants Claude Code, Codex or another local harness to use Dyna's accounts
---

# Point a local harness at Dyna

Dyna can serve its model route to harnesses on the owner's machines, so they use the same accounts and routing as boxes.

The key only exists on the owner socket, so these commands never work from a box. Run them where the owner has the `dyna` CLI: on the engine host, or on a desktop with a forwarded owner socket (`--socket PATH`). From a box, that means host exec on that machine.

## Set it up

1. Get the settings:

   ```bash
   dyna proxy env --tool claude   # or --tool codex, or no flag for both
   ```

2. Save the output where the owner keeps secrets, such as a sourced file with mode `0600`. Never put it in a repository, and don't repeat the key back. For Codex, keep the printed `codex()` function: Codex ignores `OPENAI_BASE_URL`, so the exports alone send the key to OpenAI.
3. Keep `dyna proxy serve` running on the machine with the harness, in a systemd user unit or tmux. The default port is 7791. Pass the same `--port` to `proxy env` and `proxy serve` if you change it.
4. Check it:

   ```bash
   claude -p "say hello" --model claude-opus-5-5
   dyna proxy usage
   ```

## Models

Use real model names, such as `claude-opus-5-5`, not Dyna profile names. To list them:

```bash
curl -H "Authorization: Bearer $OPENAI_API_KEY" "$OPENAI_BASE_URL/models"
```

## When it fails

| What you see | What it means |
|---|---|
| 401 | The key is stale. Run `dyna proxy env` again. Claude Code keeps retrying on a 401, so read its output rather than waiting |
| `unknown provider for model` | No account serves that model |
| `No account is connected` | The owner needs to add an account |
| Connection refused | `dyna proxy serve` isn't running, or the port differs |

## Limits

There is one key for every tool, and `dyna proxy reset` replaces it, so the saved lines need rewriting afterwards. All local harnesses share one account until it fails over.
