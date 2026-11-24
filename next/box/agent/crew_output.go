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

const structuredOutputAnyResult = `{"type":"object","properties":{"result":{}},"required":["result"],"additionalProperties":false}`

func structuredOutput(schema *jsonschema.Schema) *extension.ToolDefinition {
	// Providers don't accept the full JSON Schema dialect that workflow scripts can use,
	// so keep the wrapper minimal while hinting a single top-level type when present.
	// Avoid bare {"type":"object"} here: Antigravity's schema cleaner injects a synthetic
	// required "reason" property into property-less object schemas and strips "reason" back
	// out of tool arguments, which corrupts schemas that declare a real "reason" field.
	resultType := `{}`
	if schema.Types != nil {
		if types := schema.Types.ToStrings(); len(types) == 1 {
			switch types[0] {
			case "object":
				resultType = `{"description":"JSON object matching the result schema."}`
			case "string", "number", "integer", "boolean":
				resultType = `{"type":"` + types[0] + `"}`
			case "array":
				resultType = `{"type":"array","items":{}}`
			}
		}
	}
	return &extension.ToolDefinition{
		Name:        "StructuredOutput",
		Exposure:    extension.ToolExposureModelOnly, // a nested call can't end the worker's turn
		Description: "Return your final answer in result, matching the workflow step's schema. A valid call ends the step; if the call reports a validation error, fix it and call again.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"result":` + resultType + `},"required":["result"],"additionalProperties":false}`),
		// Let PiG pass a string-encoded JSON result through to Execute so it can unwrap and validate against the step schema.
		ValidationParameters: json.RawMessage(structuredOutputAnyResult),
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
				var encoded string
				if json.Unmarshal(input.Result, &encoded) == nil {
					if decoded, err2 := validateResult(schema, []byte(encoded)); err2 == nil {
						data, err = decoded, nil
					}
				}
			}
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
