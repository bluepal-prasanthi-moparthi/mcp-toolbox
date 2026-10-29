---
title: "ArangoDB Source"
linkTitle: "Source"
type: docs
weight: 1
description: >
  Connect MCP Toolbox to an ArangoDB database over plain HTTP.
no_list: true
---

## About

The ArangoDB source connects to one ArangoDB database using username/password
authentication over plain HTTP. The default ArangoDB HTTP port is `8529`.

{{< notice warning >}}
This implementation supports plain HTTP only. TLS/SSL and connection strings
are intentionally not supported.
{{< /notice >}}

## Available Tools

{{< list-tools >}}

## Requirements

An ArangoDB server reachable over HTTP, a database that already exists, and a
user with access to that database.

## Example

```yaml
kind: source
name: my-arangodb
type: arangodb
host: 127.0.0.1
port: 8529
database: toolbox
user: toolbox
password: ${ARANGODB_PASSWORD}
```

{{< notice tip >}}
Use `${ARANGODB_PASSWORD}` rather than storing the password in the YAML file.
{{< /notice >}}

## Reference

| **field** | **type** | **required** | **description** |
|---|:---:|:---:|---|
| type | string | true | Must be `arangodb`. |
| host | string | true | ArangoDB server hostname or IP address. |
| port | integer | true | Plain HTTP port, normally `8529`. |
| database | string | true | Existing ArangoDB database name. |
| user | string | true | ArangoDB username. |
| password | string | true | ArangoDB password; environment-variable substitution is supported. |
