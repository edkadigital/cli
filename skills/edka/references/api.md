# `edka api`

Call any Edka API endpoint, by command or by path.

## `edka api`

Call any Edka API endpoint, by command or by path.

Every Edka API endpoint a CLI token can call, plus raw requests.

Find a command with `edka api operations --search <term>`, or call a path directly
with `edka api get /api/...`.

Examples:

```bash
edka api clusters nodepools list --cluster production
edka api operations --search backup
edka api get /api/inventory/resources --json
```

## `edka api delete <path>`

DELETE an Edka API path.

Flags:

- `-d, --data string`: JSON body, @file.json or @-
- `-f, --field stringArray`: Body field: key=value or key:=JSON
- `--output-file string`: Save response privately to a file
- `-q, --query stringArray`: Query parameter: key=value

Examples:

```bash
edka api get /api/clusters --json
edka api post /api/clusters --data @cluster.json
```

## `edka api get <path>`

GET an Edka API path.

Flags:

- `-d, --data string`: JSON body, @file.json or @-
- `-f, --field stringArray`: Body field: key=value or key:=JSON
- `--output-file string`: Save response privately to a file
- `-q, --query stringArray`: Query parameter: key=value

## `edka api operations`

Search every endpoint command.

Flags:

- `--search string`: Filter commands and API paths

## `edka api patch <path>`

PATCH an Edka API path.

Flags:

- `-d, --data string`: JSON body, @file.json or @-
- `-f, --field stringArray`: Body field: key=value or key:=JSON
- `--output-file string`: Save response privately to a file
- `-q, --query stringArray`: Query parameter: key=value

## `edka api post <path>`

POST an Edka API path.

Flags:

- `-d, --data string`: JSON body, @file.json or @-
- `-f, --field stringArray`: Body field: key=value or key:=JSON
- `--output-file string`: Save response privately to a file
- `-q, --query stringArray`: Query parameter: key=value

## `edka api put <path>`

PUT an Edka API path.

Flags:

- `-d, --data string`: JSON body, @file.json or @-
- `-f, --field stringArray`: Body field: key=value or key:=JSON
- `--output-file string`: Save response privately to a file
- `-q, --query stringArray`: Query parameter: key=value

Every command also takes the global flags in SKILL.md.
