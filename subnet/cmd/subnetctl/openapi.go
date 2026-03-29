package main

import "net/http"

const openapiSpec = `{
  "openapi": "3.0.3",
  "info": {
    "title": "Subnet Proxy API",
    "description": "OpenAI-compatible proxy backed by a Gonka subnet session.",
    "version": "0.1.0"
  },
  "paths": {
    "/v1/chat/completions": {
      "post": {
        "summary": "Chat completion (OpenAI-compatible)",
        "description": "Sends a chat completion request through the subnet. Supports streaming via SSE.",
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": {
                "type": "object",
                "properties": {
                  "model": { "type": "string", "description": "Model name. Falls back to server default." },
                  "stream": { "type": "boolean", "default": false },
                  "max_tokens": { "type": "integer", "default": 2048 },
                  "messages": {
                    "type": "array",
                    "items": {
                      "type": "object",
                      "properties": {
                        "role": { "type": "string" },
                        "content": { "type": "string" }
                      }
                    }
                  }
                }
              }
            }
          }
        },
        "responses": {
          "200": { "description": "Completion response (JSON or SSE stream)" },
          "502": { "description": "Inference failed" }
        }
      }
    },
    "/v1/status": {
      "get": {
        "summary": "Session status",
        "description": "Returns escrow ID, current nonce, phase, and balance.",
        "responses": {
          "200": {
            "description": "Status",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "escrow_id": { "type": "string" },
                    "nonce": { "type": "integer" },
                    "phase": { "type": "string", "enum": ["active", "finalizing", "settlement"] },
                    "balance": { "type": "integer" }
                  }
                }
              }
            }
          }
        }
      }
    },
    "/v1/state": {
      "get": {
        "summary": "Full session state",
        "description": "Returns complete session state: config, group, all inferences with per-inference detail, host stats, revealed seeds, and warm keys.",
        "responses": {
          "200": {
            "description": "Full state snapshot",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "session": {
                      "type": "object",
                      "properties": {
                        "escrow_id": { "type": "string" },
                        "phase": { "type": "string" },
                        "balance": { "type": "integer" },
                        "latest_nonce": { "type": "integer" },
                        "finalize_nonce": { "type": "integer" },
                        "config": {
                          "type": "object",
                          "properties": {
                            "refusal_timeout": { "type": "integer" },
                            "execution_timeout": { "type": "integer" },
                            "token_price": { "type": "integer" },
                            "vote_threshold": { "type": "integer" },
                            "validation_rate": { "type": "integer" }
                          }
                        }
                      }
                    },
                    "group": {
                      "type": "array",
                      "items": {
                        "type": "object",
                        "properties": {
                          "slot_id": { "type": "integer" },
                          "validator_address": { "type": "string" }
                        }
                      }
                    },
                    "inferences": {
                      "type": "object",
                      "additionalProperties": {
                        "type": "object",
                        "properties": {
                          "status": { "type": "string" },
                          "executor_slot": { "type": "integer" },
                          "model": { "type": "string" },
                          "prompt_hash": { "type": "string" },
                          "response_hash": { "type": "string" },
                          "input_length": { "type": "integer" },
                          "max_tokens": { "type": "integer" },
                          "input_tokens": { "type": "integer" },
                          "output_tokens": { "type": "integer" },
                          "reserved_cost": { "type": "integer" },
                          "actual_cost": { "type": "integer" },
                          "started_at": { "type": "integer" },
                          "confirmed_at": { "type": "integer" },
                          "votes_valid": { "type": "integer" },
                          "votes_invalid": { "type": "integer" },
                          "validated_by": { "type": "array", "items": { "type": "integer" } }
                        }
                      }
                    },
                    "host_stats": {
                      "type": "object",
                      "additionalProperties": {
                        "type": "object",
                        "properties": {
                          "missed": { "type": "integer" },
                          "invalid": { "type": "integer" },
                          "cost": { "type": "integer" },
                          "required_validations": { "type": "integer" },
                          "completed_validations": { "type": "integer" }
                        }
                      }
                    },
                    "revealed_seeds": {
                      "type": "object",
                      "additionalProperties": { "type": "integer" }
                    },
                    "warm_keys": {
                      "type": "object",
                      "additionalProperties": { "type": "string" }
                    }
                  }
                }
              }
            }
          }
        }
      }
    },
    "/v1/finalize": {
      "post": {
        "summary": "Finalize session",
        "description": "Finalizes the subnet session and returns the settlement payload for on-chain submission.",
        "responses": {
          "200": { "description": "Settlement payload JSON" },
          "500": { "description": "Finalization failed" }
        }
      },
      "get": {
        "summary": "Retrieve settlement",
        "description": "Returns the settlement payload after POST /v1/finalize has succeeded. Only available in the settlement phase.",
        "responses": {
          "200": { "description": "Settlement payload JSON" },
          "409": { "description": "Session not yet finalized" }
        }
      }
    },
    "/v1/debug/pending": {
      "get": {
        "summary": "Pending transactions",
        "description": "Lists pending subnet transactions and warm keys.",
        "responses": {
          "200": { "description": "Pending tx list" }
        }
      }
    },
    "/v1/debug/state": {
      "get": {
        "summary": "Debug state summary",
        "description": "Returns nonce, balance, total inferences, and status counts.",
        "responses": {
          "200": { "description": "Debug state summary" }
        }
      }
    }
  }
}`

const swaggerHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>Subnet Proxy API</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    SwaggerUIBundle({ url: "/openapi.json", dom_id: "#swagger-ui" });
  </script>
</body>
</html>`

func (p *Proxy) handleSwaggerUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(swaggerHTML))
}

func (p *Proxy) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(openapiSpec))
}
