// Command generate reads VoiceBlender's openapi.yaml and writes three Go
// source files into the library root:
//
//   - models.go    — Leg, Room, Webhook structs + LegType/LegState/WebhookEventType enums
//   - requests.go  — all *Request and supporting types (PlaybackRequest excluded)
//   - responses.go — all *Response types from the spec
//
// PlaybackRequest (url/tone mutual exclusion + custom MarshalJSON) is kept in
// the hand-maintained playback.go and is not touched by this tool.
//
// Non-spec response types (AddLegResponse, ICECandidatesResponse,
// WebRTCOfferResponse) are kept in the hand-maintained responses_extra.go.
//
// Usage:
//
//	go run . -openapi /path/to/openapi.yaml -out /path/to/voice_v2-go
package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"log"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ── YAML model ────────────────────────────────────────────────────────────────

// orderedProps unmarshals a YAML mapping while preserving document key order.
type orderedProps struct {
	keys []string
	vals map[string]*Schema
}

func (op *orderedProps) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("expected mapping node, got %v", n.Kind)
	}
	op.vals = make(map[string]*Schema)
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i].Value
		var v Schema
		if err := n.Content[i+1].Decode(&v); err != nil {
			return fmt.Errorf("property %q: %w", k, err)
		}
		op.keys = append(op.keys, k)
		op.vals[k] = &v
	}
	return nil
}

// Schema represents an OpenAPI Schema Object.
type Schema struct {
	Type                 string       `yaml:"type"`
	Properties           orderedProps `yaml:"properties"`
	Required             []string     `yaml:"required"`
	Enum                 []string     `yaml:"enum"`
	Items                *Schema      `yaml:"items"`
	Ref                  string       `yaml:"$ref"`
	AdditionalProperties *Schema      `yaml:"additionalProperties"`
	Description          string       `yaml:"description"`
	Format               string       `yaml:"format"`
}

type openAPISpec struct {
	Components struct {
		Schemas map[string]*Schema `yaml:"schemas"`
	} `yaml:"components"`
}

// ── Naming helpers ────────────────────────────────────────────────────────────

// abbrevs maps lowercase word segments to idiomatic Go uppercase abbreviations.
var abbrevs = map[string]string{
	"id": "ID", "url": "URL", "uri": "URI", "sdp": "SDP",
	"tts": "TTS", "stt": "STT", "dtmf": "DTMF", "sip": "SIP",
	"api": "API", "s3": "S3", "ice": "ICE", "rtc": "RTC",
	"webrtc": "WebRTC",
}

// toCamel converts snake_case or camelCase to idiomatic Go CamelCase.
func toCamel(s string) string {
	// Insert underscores before uppercase letters to normalise camelCase input.
	var norm strings.Builder
	for i, c := range s {
		if i > 0 && c >= 'A' && c <= 'Z' {
			norm.WriteByte('_')
		}
		norm.WriteRune(c)
	}
	parts := strings.Split(strings.ToLower(norm.String()), "_")
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		if up, ok := abbrevs[p]; ok {
			b.WriteString(up)
		} else {
			b.WriteString(strings.ToUpper(p[:1]) + p[1:])
		}
	}
	return b.String()
}

// deref extracts the bare type name from a $ref like '#/components/schemas/Leg'.
func deref(ref string) string {
	parts := strings.Split(ref, "/")
	return parts[len(parts)-1]
}

// ── Per-schema customisations ─────────────────────────────────────────────────

// typeRenames maps OpenAPI schema names to different Go type names.
var typeRenames = map[string]string{
	"RoomCreateRequest": "CreateRoomRequest",
}

// fieldNameOverrides: schemaName → propName → Go field name.
var fieldNameOverrides = map[string]map[string]string{
	"Leg": {"leg_id": "ID"},
}

// fieldTypeOverrides: schemaName → propName → Go type string (overrides computed type).
var fieldTypeOverrides = map[string]map[string]string{
	// ICECandidateInit mirrors webrtc.ICECandidateInit with pointer fields for
	// optional WebRTC parameters. usernameFragment is a standard WebRTC field
	// not present in the VoiceBlender spec but required for full ICE support.
	"ICECandidateInit": {
		"sdpMid":            "*string",
		"sdpMLineIndex":     "*uint16",
		"usernameFragment":  "*string",
	},
	// auth is an inline object schema; surface it as the extracted *SIPAuth type.
	"CreateLegRequest": {
		"auth": "*SIPAuth",
	},
	// settings is a deeply nested JSON object (not flat string map).
	"DeepgramAgentRequest": {
		"settings": "json.RawMessage",
	},
}

// enumTypeRefs: schemaName → propName → Go enum type name.
// When a struct property carries an inline enum, its Go field uses this type.
var enumTypeRefs = map[string]map[string]string{
	"Leg": {
		"type":  "LegType",
		"state": "LegState",
	},
}

// goTypeName returns the Go type name for a schema name.
func goTypeName(name string) string {
	if r, ok := typeRenames[name]; ok {
		return r
	}
	return name
}

// goType converts a Schema to its Go type string.
func goType(s *Schema) string {
	if s == nil {
		return "interface{}"
	}
	if s.Ref != "" {
		return goTypeName(deref(s.Ref))
	}
	switch s.Type {
	case "string":
		return "string"
	case "integer":
		return "int"
	case "boolean":
		return "bool"
	case "number":
		return "float64"
	case "array":
		if s.Items != nil {
			return "[]" + goType(s.Items)
		}
		return "[]interface{}"
	case "object":
		if s.AdditionalProperties != nil {
			return "map[string]" + goType(s.AdditionalProperties)
		}
		return "map[string]interface{}"
	}
	return "interface{}"
}

// ── Code generation ───────────────────────────────────────────────────────────

const generatedHeader = "// Code generated by cmd/generate from openapi.yaml. DO NOT EDIT.\n\n"

// ensurePeriod appends a period to s if it does not already end with one.
func ensurePeriod(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if s[len(s)-1] != '.' {
		return s + "."
	}
	return s
}

// descFromName derives a fallback godoc description from a Go type name by
// splitting on uppercase boundaries: "CreateLegRequest" → "is a create leg request."
func descFromName(name string) string {
	var words []string
	start := 0
	for i := 1; i < len(name); i++ {
		if name[i] >= 'A' && name[i] <= 'Z' {
			words = append(words, name[start:i])
			start = i
		}
	}
	words = append(words, name[start:])
	lower := make([]string, len(words))
	for i, w := range words {
		lower[i] = strings.ToLower(w)
	}
	return "is a " + strings.Join(lower, " ") + "."
}

func genEnum(b *bytes.Buffer, typeName, constPrefix, description string, values []string) {
	fmt.Fprintf(b, "// %s %s\n", typeName, ensurePeriod(description))
	fmt.Fprintf(b, "type %s string\n\nconst (\n", typeName)
	for _, v := range values {
		name := constPrefix + toCamel(strings.NewReplacer(".", "_", "-", "_").Replace(v))
		fmt.Fprintf(b, "\t// %s is the %q %s value.\n", name, v, typeName)
		fmt.Fprintf(b, "\t%s %s = %q\n", name, typeName, v)
	}
	fmt.Fprintf(b, ")\n\n")
}

func genStruct(b *bytes.Buffer, schemaName string, s *Schema) {
	typeName := goTypeName(schemaName)
	reqSet := make(map[string]bool, len(s.Required))
	for _, r := range s.Required {
		reqSet[r] = true
	}

	if s.Description != "" {
		fmt.Fprintf(b, "// %s %s\n", typeName, ensurePeriod(s.Description))
	} else {
		fmt.Fprintf(b, "// %s %s\n", typeName, descFromName(typeName))
	}
	fmt.Fprintf(b, "type %s struct {\n", typeName)

	for _, prop := range s.Properties.keys {
		pSchema := s.Properties.vals[prop]

		// Field name.
		fieldName := toCamel(prop)
		if overrides, ok := fieldNameOverrides[schemaName]; ok {
			if n, ok := overrides[prop]; ok {
				fieldName = n
			}
		}

		// Field type — check explicit overrides first, then enum refs, then derive.
		var fieldType string
		if typeOvr, ok := fieldTypeOverrides[schemaName]; ok {
			fieldType = typeOvr[prop]
		}
		if fieldType == "" {
			if enumRefs, ok := enumTypeRefs[schemaName]; ok {
				fieldType = enumRefs[prop]
			}
		}
		if fieldType == "" {
			fieldType = goType(pSchema)
		}

		// JSON tag.
		tag := prop
		if !reqSet[prop] {
			tag += ",omitempty"
		}

		// Field comment from OpenAPI description.
		if pSchema.Description != "" {
			fmt.Fprintf(b, "\t// %s\n", ensurePeriod(pSchema.Description))
		}

		fmt.Fprintf(b, "\t%s %s `json:%q`\n", fieldName, fieldType, tag)
	}
	fmt.Fprintf(b, "}\n\n")
}

// ── File generators ───────────────────────────────────────────────────────────

func genModels(schemas map[string]*Schema) []byte {
	var b bytes.Buffer
	b.WriteString(generatedHeader)
	b.WriteString("package voiceblender\n\n")

	// LegType — derived from Leg.properties.type.enum
	genEnum(&b, "LegType", "LegType", "identifies the type of a voice leg.",
		schemas["Leg"].Properties.vals["type"].Enum)

	// LegState — derived from Leg.properties.state.enum.
	// LegStatePending is kept for legs that have been created but not yet ringing.
	legStateVals := schemas["Leg"].Properties.vals["state"].Enum
	genEnum(&b, "LegState", "LegState", "is the current state of a leg.",
		append([]string{"pending"}, legStateVals...))

	// WebhookEventType — top-level string enum schema.
	genEnum(&b, "WebhookEventType", "Event", "is the type of a webhook event.",
		schemas["WebhookEventType"].Enum)

	// Core resource structs.
	for _, name := range []string{"Leg", "Room"} {
		s, ok := schemas[name]
		if !ok {
			log.Printf("warning: schema %q not found, skipping", name)
			continue
		}
		genStruct(&b, name, s)
	}

	return fmtGo(b.Bytes())
}

func genRequests(schemas map[string]*Schema) []byte {
	var b bytes.Buffer
	b.WriteString(generatedHeader)
	b.WriteString("package voiceblender\n\n")
	b.WriteString("import \"encoding/json\"\n\n")

	// SIPAuth is an inline schema within CreateLegRequest.auth; emit it first.
	b.WriteString("// SIPAuth holds SIP digest authentication credentials.\n")
	b.WriteString("type SIPAuth struct {\n")
	b.WriteString("\tUsername string `json:\"username\"`\n")
	b.WriteString("\tPassword string `json:\"password\"`\n")
	b.WriteString("}\n\n")

	// Request schemas in declaration order. PlaybackRequest is excluded — it
	// lives in the hand-maintained playback.go (custom MarshalJSON).
	// ICECandidateInit is excluded below (hardcoded to add usernameFragment,
	// a standard WebRTC field absent from the spec).
	requestSchemas := []string{
		"CreateLegRequest",
		"DTMFRequest",
		"VolumeRequest",
		"TTSRequest",
		"STTRequest",
		"DeepgramAgentRequest",
		"ElevenLabsAgentRequest",
		"PipecatAgentRequest",
		"VAPIAgentRequest",
		"AgentMessageRequest",
		"RecordingRequest",
		"WebRTCOfferRequest",
		"RoomCreateRequest",
		"AddLegRequest",
	}
	for _, name := range requestSchemas {
		s, ok := schemas[name]
		if !ok {
			log.Printf("warning: schema %q not found, skipping", name)
			continue
		}
		genStruct(&b, name, s)
	}

	// ICECandidateInit — hardcoded to include usernameFragment, a standard
	// WebRTC field that is part of RTCIceCandidateInit but absent from the spec.
	b.WriteString("// ICECandidateInit is a WebRTC ICE candidate initialisation struct.\n")
	b.WriteString("type ICECandidateInit struct {\n")
	b.WriteString("\tCandidate        string  `json:\"candidate\"`\n")
	b.WriteString("\tSDPMid           *string `json:\"sdpMid,omitempty\"`\n")
	b.WriteString("\tSDPMLineIndex    *uint16 `json:\"sdpMLineIndex,omitempty\"`\n")
	b.WriteString("\tUsernameFragment *string `json:\"usernameFragment,omitempty\"`\n")
	b.WriteString("}\n\n")

	return fmtGo(b.Bytes())
}

func genResponses(schemas map[string]*Schema) []byte {
	var b bytes.Buffer
	b.WriteString(generatedHeader)
	b.WriteString("package voiceblender\n\n")

	responseSchemas := []string{
		"StatusResponse",
	}
	for _, name := range responseSchemas {
		s, ok := schemas[name]
		if !ok {
			log.Printf("warning: schema %q not found, skipping", name)
			continue
		}
		genStruct(&b, name, s)
	}

	return fmtGo(b.Bytes())
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func fmtGo(src []byte) []byte {
	out, err := format.Source(src)
	if err != nil {
		// Return the unformatted source so the error is visible in the output.
		log.Printf("warning: gofmt failed: %v\n--- source ---\n%s", err, src)
		return src
	}
	return out
}

func write(path string, data []byte) {
	if err := os.WriteFile(path, data, 0o644); err != nil {
		log.Fatalf("write %s: %v", path, err)
	}
	log.Printf("wrote %s", path)
}

// ── Entry point ───────────────────────────────────────────────────────────────

func main() {
	openapi := flag.String("openapi", "", "path to openapi.yaml (required)")
	out := flag.String("out", ".", "output directory for generated .go files")
	flag.Parse()

	if *openapi == "" {
		flag.Usage()
		os.Exit(1)
	}

	raw, err := os.ReadFile(*openapi)
	if err != nil {
		log.Fatalf("read %s: %v", *openapi, err)
	}

	var spec openAPISpec
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		log.Fatalf("parse openapi.yaml: %v", err)
	}

	schemas := spec.Components.Schemas

	write(filepath.Join(*out, "models.go"), genModels(schemas))
	write(filepath.Join(*out, "requests.go"), genRequests(schemas))
	write(filepath.Join(*out, "responses.go"), genResponses(schemas))
}
