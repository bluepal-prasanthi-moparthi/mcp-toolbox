---
title: "arangodb-execute-aql"
type: docs
weight: 1
description: >
  Execute a dynamic AQL query against ArangoDB.
---

## About

`arangodb-execute-aql` accepts a complete AQL query at invocation time. It
supports read and write queries and sends `bindVars` to ArangoDB as native AQL
bind variables. Object and scalar rows are preserved in the `results` array.

{{< notice warning >}}
This tool permits arbitrary AQL, including writes and administrative operations
allowed by the configured ArangoDB user. Use a least-privilege account.
{{< /notice >}}

## Compatible Sources

{{< compatible-sources >}}

## Parameters

| **parameter** | **type** | **required** | **description** |
|---|:---:|:---:|---|
| query | string | true | A complete AQL query to execute. |
| bindVars | object | false | AQL bind variables. Collection binds for `@@collection` use the `@collection` key. |

## Example

```yaml
kind: tool
name: execute-aql
type: arangodb-execute-aql
source: my-arangodb
description: Execute a dynamic AQL query against ArangoDB.
```

Invoke it with an MCP input payload such as:

```json
{
  "query": "FOR doc IN @@collection FILTER doc.status == @status LIMIT @limit RETURN doc",
  "bindVars": {
    "@collection": "customers",
    "status": "active",
    "limit": 10
  }
}
```

## Output Format

The tool returns a JSON-compatible object:

```json
{
  "results": [{"name": "toolbox"}, 1, null],
  "count": 3
}
```

For no returned rows, `results` is `[]` and `count` is `0`.

## Reference

| **field** | **type** | **required** | **description** |
|---|:---:|:---:|---|
| type | string | true | Must be `arangodb-execute-aql`. |
| source | string | true | Name of an ArangoDB source. |
| description | string | true | Tool description shown to MCP clients. |
