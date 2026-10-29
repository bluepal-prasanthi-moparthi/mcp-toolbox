---
title: "ArangoDB"
type: docs
description: "Details of the ArangoDB prebuilt configuration."
---

## ArangoDB

*   `--prebuilt` value: `arangodb`
*   **Environment Variables:**
    *   `ARANGODB_HOST`: ArangoDB server hostname or IP address (default: `127.0.0.1`).
    *   `ARANGODB_PORT`: Plain HTTP port (default: `8529`).
    *   `ARANGODB_DATABASE`: Name of an existing ArangoDB database.
    *   `ARANGODB_USER`: ArangoDB username.
    *   `ARANGODB_PASSWORD`: ArangoDB password.
*   **Permissions:**
    *   The configured user must be allowed to run the AQL executed through the tool.
*   **Tools:**
    *   `execute_aql`: Executes a dynamic AQL query with optional bind variables.
