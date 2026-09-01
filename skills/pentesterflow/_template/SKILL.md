---
name: my-skill
description: One line on what this playbook does, then a "Use when ..." clause so the agent knows when to load it (e.g. "Use when the target exposes X / you see Y in requests"). Max 1024 chars. This description is the ONLY thing the model sees until it loads the skill — make the trigger conditions explicit, since there is no separate `triggers` list.
allowed-tools:
  - http
  - shell
  - file_write
---

# my-skill playbook

State the goal in one or two sentences — what the operator is trying to
achieve — and the scope rules (authorized targets only).

## 1. First step

Concrete, copy-pasteable commands. Default to curl + the `http` tool; only
reach for specialised scanners when the user asks.

```sh
curl -ksS "https://TARGET/..."
```

Reference bundled files (e.g. a wordlist under `payloads/`) with
`read_payloads(skill="my-skill", file="list.txt")`, or shell scripts via the
`${SKILL_DIR}` placeholder:

```sh
${SKILL_DIR}/scripts/check.sh https://TARGET
```

## 2. Next step

...

## Reporting

What proves the bug, the concrete impact in one sentence, and remediation.
When you have a reproduced finding with a real request/response, call
`confirm_finding`.
