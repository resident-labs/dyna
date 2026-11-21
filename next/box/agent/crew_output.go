package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func structuredOutput(schema *jsonschema.Schema) *extension.ToolDefinition {
	return &extension.ToolDefinition{
		Name:        "StructuredOutput",
		Exposure:    extension.ToolExposureModelOnly, // a nested call can't end the worker's turn
		Description: "Return your final answer in result, matching the workflow step's schema. A valid call ends the step; if the call reports a validation error, fix it and call again.",
		// Providers don't accept the full JSON Schema dialect that workflow scripts can use.
		Parameters: json.RawMessage(`{"type":"object","properties":{"result":{}},"required":["result"],"additionalProperties":false}`),
		Execute: func(_ context.Context, _ string, args json.RawMessage, _ extension.AgentToolUpdateCallback) (extension.AgentToolResult, error) {
			var input struct {
				Result json.RawMessage `json:"result"`
			}
			if err := json.Unmarshal(args, &input); err != nil {
				return extension.AgentToolResult{}, err
			}
			if input.Result == nil {
				return extension.AgentToolResult{}, errors.New("put your answer in the result argument")
			}
			data, err := validateResult(schema, input.Result)
			if err != nil {
				return extension.AgentToolResult{}, err
			}
			return extension.AgentToolResult{
				Content: []ai.ToolResultMessageContent{ai.TextContent{Text: "Accepted."}},
				Details: data,
			}, nil
		},
	}
}

func validateResult(schema *jsonschema.Schema, raw []byte) (json.RawMessage, error) {
	if len(raw) > stepResultBytes {
		return nil, fmt.Errorf("the result is %d bytes, over the limit of %d; write it to a file and return its path in the shape the schema allows", len(raw), stepResultBytes)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("it is not JSON: %w", err)
	}
	if err := schema.Validate(value); err != nil {
		return nil, err
	}
	data, err := json.Marshal(value)
	if len(data) > stepResultBytes {
		return nil, fmt.Errorf("the encoded result is %d bytes, over the limit of %d; write it to a file and return its path in the shape the schema allows", len(data), stepResultBytes)
	}
	return data, err
}
