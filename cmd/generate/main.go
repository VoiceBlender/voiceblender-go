// Command generate reads VoiceBlender's openapi.yaml and writes Go source
// files into the library root:
//
//   - models.go    — Leg, Room, Webhook structs + LegType/LegState/WebhookEventType enums
//   - requests.go  — all *Request and supporting types (PlaybackRequest excluded)
//   - responses.go — all *Response types from the spec
//   - events.go    — typed event structs from x-webhooks + ParseEvent dispatcher
//   - legs.go      — Client methods for /legs endpoints
//   - rooms.go     — Client methods for /rooms endpoints
//   - webrtc.go    — Client methods for /webrtc endpoints
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
	"regexp"
	"sort"
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
	AllOf                []*Schema    `yaml:"allOf"`
	AdditionalProperties *Schema      `yaml:"additionalProperties"`
	Description          string       `yaml:"description"`
	Format               string       `yaml:"format"`
	Nullable             bool         `yaml:"nullable"`
}

// ── Path/Operation YAML types ────────────────────────────────────────────────

// PathItem represents an OpenAPI Path Item Object.
type PathItem struct {
	Get    *Operation `yaml:"get"`
	Post   *Operation `yaml:"post"`
	Put    *Operation `yaml:"put"`
	Patch  *Operation `yaml:"patch"`
	Delete *Operation `yaml:"delete"`
}

// Operation represents an OpenAPI Operation Object.
type Operation struct {
	OperationID string         `yaml:"operationId"`
	Summary     string         `yaml:"summary"`
	Tags        []string       `yaml:"tags"`
	RequestBody *OpRequestBody `yaml:"requestBody"`
	Responses   map[string]*OpResponse `yaml:"responses"`
}

// OpRequestBody represents an OpenAPI Request Body Object.
type OpRequestBody struct {
	Content map[string]*OpMedia `yaml:"content"`
}

// OpMedia represents an OpenAPI Media Type Object.
type OpMedia struct {
	Schema *Schema `yaml:"schema"`
}

// OpResponse represents an OpenAPI Response Object.
type OpResponse struct {
	Content map[string]*OpMedia `yaml:"content"`
}

// orderedPaths unmarshals the paths mapping while preserving document order.
type orderedPaths struct {
	keys []string
	vals map[string]*PathItem
}

func (op *orderedPaths) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("expected mapping node for paths, got %v", n.Kind)
	}
	op.vals = make(map[string]*PathItem)
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i].Value
		var v PathItem
		if err := n.Content[i+1].Decode(&v); err != nil {
			return fmt.Errorf("path %q: %w", k, err)
		}
		op.keys = append(op.keys, k)
		op.vals[k] = &v
	}
	return nil
}

// orderedWebhooks unmarshals x-webhooks while preserving document order.
type orderedWebhooks struct {
	keys []string
	vals map[string]*PathItem
}

func (ow *orderedWebhooks) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("expected mapping node for x-webhooks, got %v", n.Kind)
	}
	ow.vals = make(map[string]*PathItem)
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i].Value
		var v PathItem
		if err := n.Content[i+1].Decode(&v); err != nil {
			return fmt.Errorf("webhook %q: %w", k, err)
		}
		ow.keys = append(ow.keys, k)
		ow.vals[k] = &v
	}
	return nil
}

type openAPISpec struct {
	Paths      orderedPaths    `yaml:"paths"`
	XWebhooks  orderedWebhooks `yaml:"x-webhooks"`
	Components struct {
		Schemas map[string]*Schema `yaml:"schemas"`
	} `yaml:"components"`
}

// ── AsyncAPI 3.0 model ────────────────────────────────────────────────────────
//
// Only the parts of the spec we actually use for VSI command generation are
// modelled here: channels (just to walk message refs), operations (recv_* are
// the client-callable commands), and the per-message envelope schemas under
// components.messages.

// orderedAAOperations preserves the order of operations: <name> entries.
type orderedAAOperations struct {
	keys []string
	vals map[string]*aaOperation
}

func (oo *orderedAAOperations) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("expected mapping node for operations, got %v", n.Kind)
	}
	oo.vals = make(map[string]*aaOperation)
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i].Value
		var v aaOperation
		if err := n.Content[i+1].Decode(&v); err != nil {
			return fmt.Errorf("operation %q: %w", k, err)
		}
		oo.keys = append(oo.keys, k)
		oo.vals[k] = &v
	}
	return nil
}

type asyncAPISpec struct {
	Channels   map[string]*aaChannel `yaml:"channels"`
	Operations orderedAAOperations   `yaml:"operations"`
	Components struct {
		Messages map[string]*aaMessage `yaml:"messages"`
		Schemas  map[string]*Schema    `yaml:"schemas"`
	} `yaml:"components"`
}

// aaChannel holds the messages map for one channel. The channel hop is purely
// organisational — each entry is a $ref into components.messages — but we
// need it because reply message refs in operations point at the channel
// (e.g. #/channels/vsi/messages/list_legs.result), not directly at components.
type aaChannel struct {
	Address  string            `yaml:"address"`
	Messages map[string]*aaRef `yaml:"messages"`
}

// aaRef is a single-field $ref wrapper.
type aaRef struct {
	Ref string `yaml:"$ref"`
}

// aaOperation models a `send_*` (server→client event) or `recv_*`
// (client→server command) operation.
type aaOperation struct {
	Action   string   `yaml:"action"` // "send" | "receive"
	Channel  *aaRef   `yaml:"channel"`
	Summary  string   `yaml:"summary"`
	Messages []*aaRef `yaml:"messages"`
	Reply    *aaReply `yaml:"reply"`
}

// aaReply is the operation's reply field; for recv_* it lists `<cmd>.result`
// and `error` messages.
type aaReply struct {
	Channel  *aaRef   `yaml:"channel"`
	Messages []*aaRef `yaml:"messages"`
}

// aaMessage is a components.messages.<name> entry. Payload is the JSON Schema
// of the wire frame (with `type`, `request_id`, and `payload`/`data`).
type aaMessage struct {
	Name    string  `yaml:"name"`
	Title   string  `yaml:"title"`
	Summary string  `yaml:"summary"`
	Payload *Schema `yaml:"payload"`
}

// refTail returns the last segment of a $ref path (e.g.
// "#/components/messages/send_leg_rtt" → "send_leg_rtt"). It also returns
// false when the ref is cross-file (e.g. "openapi.yaml#/...") because the
// referenced type lives outside this spec and we cannot resolve it locally.
func refTail(ref string) (string, bool) {
	if ref == "" {
		return "", false
	}
	// Cross-file refs (containing a path before the '#') we cannot resolve.
	if i := strings.Index(ref, "#"); i > 0 {
		return "", false
	}
	if strings.HasPrefix(ref, "#/") {
		ref = ref[2:]
	}
	parts := strings.Split(ref, "/")
	return parts[len(parts)-1], true
}

// ── Naming helpers ────────────────────────────────────────────────────────────

// abbrevs maps lowercase word segments to idiomatic Go uppercase abbreviations.
var abbrevs = map[string]string{
	"id": "ID", "url": "URL", "uri": "URI", "sdp": "SDP",
	"tts": "TTS", "stt": "STT", "dtmf": "DTMF", "sip": "SIP",
	"api": "API", "s3": "S3", "ice": "ICE", "rtc": "RTC",
	"webrtc": "WebRTC", "amd": "AMD", "rtt": "RTT", "vsi": "VSI",
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
		"sdpMid":           "*string",
		"sdpMLineIndex":    "*uint16",
		"usernameFragment": "*string",
	},
	// auth is an inline object schema; surface it as the extracted *SIPAuth type.
	// amd and the boolean tri-state fields need pointer types so callers can
	// distinguish "unset / use server default" from an explicit zero value.
	"CreateLegRequest": {
		"auth":             "*SIPAuth",
		"amd":              "*AMDParams",
		"accept_dtmf":      "*bool",
		"speech_detection": "*bool",
	},
	"AnswerLegRequest": {
		"speech_detection": "*bool",
	},
	// All three AddLegRequest booleans are tri-state: "Omit to leave current
	// state untouched" — so callers need to distinguish unset from an explicit
	// false.
	"AddLegRequest": {
		"mute":        "*bool",
		"deaf":        "*bool",
		"accept_dtmf": "*bool",
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

// goTypeName returns the Go type name for a schema name. AsyncAPI uses
// lowerCamelCase schema names (rttPayload, vsiStatusResponse, …) which we
// need to convert to idiomatic Go via toCamel; OpenAPI schemas are already
// CamelCased and pass through.
func goTypeName(name string) string {
	if r, ok := typeRenames[name]; ok {
		return r
	}
	if name != "" && name[0] >= 'a' && name[0] <= 'z' {
		return toCamel(name)
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

// genStruct emits a Go struct for the given schema. extraFields are appended
// verbatim before the closing brace (e.g. unexported fields not present in the
// OpenAPI spec, like the back-reference to *Client on Leg/Room).
func genStruct(b *bytes.Buffer, schemaName string, s *Schema, extraFields ...string) {
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
	for _, ef := range extraFields {
		fmt.Fprintf(b, "\t%s\n", ef)
	}
	fmt.Fprintf(b, "}\n\n")
}

// ── File generators ───────────────────────────────────────────────────────────

// genModels emits models.go. asyncDefined holds the names of schemas defined in
// asyncapi.yaml; placeholder aliases are suppressed for those because genVSI
// emits a real struct for them in vsi.go (avoiding a redeclaration).
func genModels(schemas map[string]*Schema, asyncDefined map[string]bool) []byte {
	var b bytes.Buffer
	b.WriteString(generatedHeader)
	b.WriteString("package voiceblender\n\n")
	b.WriteString("import \"encoding/json\"\n\n")

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

	// Type alias for schemas referenced but not fully defined in the spec.
	// Skip names defined in asyncapi.yaml: genVSI emits a real struct for those
	// in vsi.go, so emitting a placeholder here would redeclare the type.
	for _, name := range []string{"ChannelInfo", "OfferedCodec"} {
		if _, ok := schemas[name]; ok {
			continue
		}
		if asyncDefined[name] {
			continue
		}
		fmt.Fprintf(&b, "// %s is referenced in the spec but not fully defined; use json.RawMessage to decode.\n", name)
		fmt.Fprintf(&b, "type %s = json.RawMessage\n\n", name)
	}

	// Core resource structs. Leg and Room carry an unexported back-reference to
	// the *Client that produced them so receiver methods (l.Mute, r.Play, etc.)
	// can issue HTTP calls without the caller threading the client through.
	// The field is unexported and has no JSON tag, so encoding/json ignores it.
	for _, name := range []string{"Leg", "Room"} {
		s, ok := schemas[name]
		if !ok {
			log.Printf("warning: schema %q not found, skipping", name)
			continue
		}
		genStruct(&b, name, s, "client *Client")
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
		"AnswerLegRequest",
		"EarlyMediaLegRequest",
		"DeleteLegRequest",
		"TransferRequest",
		"DTMFRequest",
		"RTTRequest",
		"VolumeRequest",
		"TTSRequest",
		"STTRequest",
		"DeepgramAgentRequest",
		"ElevenLabsAgentRequest",
		"PipecatAgentRequest",
		"VAPIAgentRequest",
		"AgentMessageRequest",
		"AMDParams",
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

// ── Event type generation from x-webhooks ────────────────────────────────────

// webhookEventInfo holds the parsed data for one x-webhook entry.
type webhookEventInfo struct {
	eventName string // e.g. "leg.ringing"
	summary   string
	props     orderedProps
	required  map[string]bool
}

// eventTypeName converts "leg.ringing" → "LegRingingEvent".
func eventTypeName(name string) string {
	return toCamel(strings.NewReplacer(".", "_", "-", "_").Replace(name)) + "Event"
}

// extractWebhooks parses x-webhooks into a slice of webhookEventInfo.
func extractWebhooks(wh orderedWebhooks) []webhookEventInfo {
	var events []webhookEventInfo
	for _, name := range wh.keys {
		item := wh.vals[name]
		op := item.Post
		if op == nil {
			continue
		}
		if op.RequestBody == nil {
			continue
		}
		media, ok := op.RequestBody.Content["application/json"]
		if !ok || media.Schema == nil {
			continue
		}
		s := media.Schema

		info := webhookEventInfo{
			eventName: name,
			summary:   op.Summary,
			required:  make(map[string]bool),
		}

		// The schema is allOf: [WebhookEvent ref, inline properties].
		// We only want the inline properties (skip $ref entries).
		for _, part := range s.AllOf {
			if part.Ref != "" {
				continue
			}
			info.props = part.Properties
			for _, r := range part.Required {
				info.required[r] = true
			}
		}

		events = append(events, info)
	}
	return events
}

// genNestedStruct generates an inline struct type string for an object property.
func genNestedStruct(s *Schema) string {
	var b strings.Builder
	b.WriteString("struct {\n")
	reqSet := make(map[string]bool, len(s.Required))
	for _, r := range s.Required {
		reqSet[r] = true
	}
	for _, prop := range s.Properties.keys {
		ps := s.Properties.vals[prop]
		fieldName := toCamel(prop)
		fieldType := goType(ps)
		if ps.Type == "object" && ps.Properties.keys != nil {
			fieldType = genNestedStruct(ps)
		}
		tag := prop
		if !reqSet[prop] {
			tag += ",omitempty"
		}
		if ps.Description != "" {
			fmt.Fprintf(&b, "\t\t// %s\n", ensurePeriod(ps.Description))
		}
		fmt.Fprintf(&b, "\t\t%s %s `json:%q`\n", fieldName, fieldType, tag)
	}
	b.WriteString("\t}")
	return b.String()
}

func genEvents(webhooks orderedWebhooks) []byte {
	events := extractWebhooks(webhooks)
	if len(events) == 0 {
		return nil
	}

	var b bytes.Buffer
	b.WriteString(generatedHeader)
	b.WriteString("package voiceblender\n\n")
	b.WriteString("import (\n")
	b.WriteString("\t\"encoding/json\"\n")
	b.WriteString("\t\"fmt\"\n")
	b.WriteString("\t\"time\"\n")
	b.WriteString(")\n\n")

	// Base Event struct matching WebhookEvent schema.
	b.WriteString("// Event is the base envelope for all webhook/WebSocket events.\n")
	b.WriteString("type Event struct {\n")
	b.WriteString("\tType       WebhookEventType `json:\"type\"`\n")
	b.WriteString("\tTimestamp  time.Time         `json:\"timestamp\"`\n")
	b.WriteString("\tInstanceID string            `json:\"instance_id,omitempty\"`\n")
	b.WriteString("}\n\n")

	// Per-event structs.
	for _, ev := range events {
		typeName := eventTypeName(ev.eventName)
		if ev.summary != "" {
			fmt.Fprintf(&b, "// %s is fired when: %s\n", typeName, strings.ToLower(ev.summary[:1])+ev.summary[1:])
		}
		fmt.Fprintf(&b, "type %s struct {\n", typeName)
		b.WriteString("\tEvent\n")

		for _, prop := range ev.props.keys {
			ps := ev.props.vals[prop]
			fieldName := toCamel(prop)
			fieldType := goType(ps)

			// Handle nested object properties with known structure.
			if ps.Type == "object" && ps.Properties.keys != nil {
				if ps.Nullable {
					fieldType = "*" + genNestedStruct(ps)
				} else {
					fieldType = genNestedStruct(ps)
				}
			}

			tag := prop
			if !ev.required[prop] {
				tag += ",omitempty"
			}
			if ps.Description != "" {
				fmt.Fprintf(&b, "\t// %s\n", ensurePeriod(ps.Description))
			}
			fmt.Fprintf(&b, "\t%s %s `json:%q`\n", fieldName, fieldType, tag)
		}

		b.WriteString("}\n\n")
	}

	// ParseEvent unmarshals raw JSON into the correct typed event struct.
	b.WriteString("// ParseEvent unmarshals raw JSON into the appropriate typed event struct.\n")
	b.WriteString("func ParseEvent(data []byte) (interface{}, error) {\n")
	b.WriteString("\tvar base Event\n")
	b.WriteString("\tif err := json.Unmarshal(data, &base); err != nil {\n")
	b.WriteString("\t\treturn nil, fmt.Errorf(\"parse event envelope: %w\", err)\n")
	b.WriteString("\t}\n")
	b.WriteString("\tswitch base.Type {\n")

	for _, ev := range events {
		typeName := eventTypeName(ev.eventName)
		constName := "Event" + toCamel(strings.NewReplacer(".", "_", "-", "_").Replace(ev.eventName))
		fmt.Fprintf(&b, "\tcase %s:\n", constName)
		fmt.Fprintf(&b, "\t\tvar e %s\n", typeName)
		b.WriteString("\t\tif err := json.Unmarshal(data, &e); err != nil {\n")
		b.WriteString("\t\t\treturn nil, err\n")
		b.WriteString("\t\t}\n")
		b.WriteString("\t\treturn &e, nil\n")
	}

	b.WriteString("\tdefault:\n")
	b.WriteString("\t\treturn &base, nil\n")
	b.WriteString("\t}\n")
	b.WriteString("}\n")

	return fmtGo(b.Bytes())
}

// ── Client method generation from paths ──────────────────────────────────────

// opInfo holds everything needed to generate a Client method.
type opInfo struct {
	operationID string
	httpMethod  string // "GET", "POST", etc.
	path        string // URL path template e.g. "/legs/{id}/amd"
	summary     string
	tag         string   // first tag
	reqType     string   // Go request type (empty = no body)
	respType    string   // Go response type (without * or [])
	respSlice   bool     // true if response is []Type
	pathParams  []string // path params after applying receiver scope (e.g. ["playbackID"])
	// receiver = "Leg", "Room", or "" (= Client). When non-empty, the first
	// {id} in the path is consumed by the receiver's ID field and the method
	// is generated with the matching pointer receiver.
	receiver string
}

// resourceScope inspects a path and decides whether the operation belongs on
// a *Leg, *Room, or stays on *Client. It returns the receiver type ("Leg",
// "Room", or "") and the slice of path params remaining after the leading
// resource ID is consumed by the receiver. For "/legs/{id}/play/{playbackID}"
// it returns ("Leg", ["playbackID"]); for "/legs" it returns ("", nil).
func resourceScope(path string, params []string) (string, []string) {
	scopes := []struct {
		prefix string
		recv   string
	}{
		{"/legs/{id}", "Leg"},
		{"/rooms/{id}", "Room"},
	}
	for _, sc := range scopes {
		if path == sc.prefix || strings.HasPrefix(path, sc.prefix+"/") {
			rest := []string{}
			if len(params) > 0 {
				rest = append(rest, params[1:]...)
			}
			return sc.recv, rest
		}
	}
	return "", params
}

var pathParamRe = regexp.MustCompile(`\{(\w+)\}`)

// extractPathParams returns parameter names from a path template.
func extractPathParams(path string) []string {
	matches := pathParamRe.FindAllStringSubmatch(path, -1)
	var params []string
	for _, m := range matches {
		params = append(params, m[1])
	}
	return params
}

// buildGoPath converts a path template into a Go expression that concatenates
// literals with parameter expressions. Path params are referenced by their
// name unless overridden via subs (e.g. {"id": "l.ID"} for receiver methods).
//
//	"/legs/{id}/play/{playbackID}", nil          → "/legs/"+id+"/play/"+playbackID
//	"/legs/{id}/mute", {"id": "l.ID"}            → "/legs/"+l.ID+"/mute"
func buildGoPath(path string, subs map[string]string) string {
	parts := pathParamRe.Split(path, -1)
	params := pathParamRe.FindAllStringSubmatch(path, -1)
	var b strings.Builder
	for i, lit := range parts {
		if i > 0 {
			name := params[i-1][1]
			expr := name
			if sub, ok := subs[name]; ok {
				expr = sub
			}
			b.WriteString("+" + expr)
			if lit != "" {
				b.WriteString("+")
			}
		}
		if lit != "" {
			b.WriteString(fmt.Sprintf("%q", lit))
		}
	}
	return b.String()
}

// httpMethodConst maps lowercase HTTP verbs to net/http constants.
var httpMethodConst = map[string]string{
	"GET":    "http.MethodGet",
	"POST":   "http.MethodPost",
	"PUT":    "http.MethodPut",
	"PATCH":  "http.MethodPatch",
	"DELETE": "http.MethodDelete",
}

// methodNameOverrides: operationId → Go method name (when toCamel is wrong
// or when the Leg/Room suffix should be dropped for receiver methods).
//
// Methods scoped to /legs/{id}/... and /rooms/{id}/... are emitted on *Leg
// and *Room respectively, so the trailing "Leg"/"Room" in the operationId is
// redundant — strip it here. Operations on *Client (no ID in path) keep
// their full names (e.g. createLeg → CreateLeg, listRooms → ListRooms).
var methodNameOverrides = map[string]string{
	// Leg-scoped: drop "Leg" suffix.
	"deleteLeg":          "Hangup",
	"answerLeg":          "Answer",
	"earlyMediaLeg":      "EarlyMedia",
	"ringLeg":            "Ring",
	"muteLeg":            "Mute",
	"unmuteLeg":          "Unmute",
	"holdLeg":            "Hold",
	"unholdLeg":          "Unhold",
	"transferLeg":        "Transfer",
	// "Accept" / "Reject" would collide with the Leg.AcceptDTMF data field; use
	// the toggle verbs instead.
	"acceptDTMFLeg": "EnableDTMF",
	"rejectDTMFLeg": "DisableDTMF",
	"playLeg":            "Play",
	"volumePlayLeg":      "VolumePlay",
	"stopPlayLeg":        "StopPlay",
	"ttsLeg":             "PlayTTS",
	"recordLeg":          "Record",
	"stopRecordLeg":      "StopRecord",
	"pauseRecordLeg":     "PauseRecord",
	"resumeRecordLeg":    "ResumeRecord",
	"sttLeg":             "STT",
	"stopSTTLeg":         "StopSTT",
	"stopAgentLeg":       "StopAgent",
	"startAMDLeg":        "StartAMD",
	"agentLegElevenLabs": "ElevenLabsAgent",
	"agentLegVAPI":       "VAPIAgent",
	"agentLegPipecat":    "PipecatAgent",
	"agentLegDeepgram":   "DeepgramAgent",
	"agentLegMessage":    "AgentMessage",

	// Room-scoped: drop "Room" suffix.
	"deleteRoom":          "Delete",
	"addLegToRoom":        "AddLeg",
	"removeLegFromRoom":   "RemoveLeg",
	"playRoom":            "Play",
	"volumePlayRoom":      "VolumePlay",
	"stopPlayRoom":        "StopPlay",
	"ttsRoom":             "PlayTTS",
	"recordRoom":          "Record",
	"stopRecordRoom":      "StopRecord",
	"pauseRecordRoom":     "PauseRecord",
	"resumeRecordRoom":    "ResumeRecord",
	"sttRoom":             "STT",
	"stopSTTRoom":         "StopSTT",
	"stopAgentRoom":       "StopAgent",
	"agentRoomElevenLabs": "ElevenLabsAgent",
	"agentRoomVAPI":       "VAPIAgent",
	"agentRoomPipecat":    "PipecatAgent",
	"agentRoomDeepgram":   "DeepgramAgent",
	"agentRoomMessage":    "AgentMessage",

	// Other Leg-scoped operationIds that don't carry the suffix
	// (sendDTMF, getICECandidates, addICECandidate) keep their toCamel default
	// and become methods on *Leg automatically: SendDTMF, GetICECandidates,
	// AddICECandidate.
}

// forceClientReceiver: operationIds whose path matches a resource scope but
// which should still be emitted as a Client method (not on *Leg / *Room).
// getLeg and getRoom are the canonical "fetch resource by ID" calls, so they
// stay on *Client where callers naturally invoke them with just the ID.
var forceClientReceiver = map[string]bool{
	"getLeg":  true,
	"getRoom": true,
}

// responseTypeOverrides: operationId → Go response type.
// Used when the spec says StatusResponse but the server returns a richer type
// defined in hand-maintained responses_extra.go.
var responseTypeOverrides = map[string]string{
	"playLeg":          "PlaybackResponse",
	"ttsLeg":           "TTSResponse",
	"recordLeg":        "RecordingResponse",
	"stopRecordLeg":    "RecordingResponse",
	"playRoom":         "PlaybackResponse",
	"ttsRoom":          "TTSResponse",
	"recordRoom":       "RecordingResponse",
	"stopRecordRoom":   "RecordingResponse",
	"addLegToRoom":     "AddLegResponse",
	"webrtcOffer":      "WebRTCOfferResponse",
	"getICECandidates": "ICECandidatesResponse",
}

// requestTypeOverrides: operationId → Go request type.
// Used when the spec doesn't include a requestBody but the client sends one.
var requestTypeOverrides = map[string]string{
	"addICECandidate": "ICECandidateInit",
}

// skipOperations are not generated (websocket, observability, etc.).
var skipOperations = map[string]bool{
	"wsRoom":          true,
	"vsi":             true,
	"getMetrics":      true,
	"pprofIndex":      true,
	"pprofCPU":        true,
	"pprofHeap":       true,
	"pprofGoroutine":  true,
}

// tagFile maps an OpenAPI tag to the output Go filename.
var tagFile = map[string]string{
	"Legs":   "legs.go",
	"Rooms":  "rooms.go",
	"WebRTC": "webrtc.go",
}

// extractOps walks the parsed paths and returns operations grouped by tag.
func extractOps(paths orderedPaths) []opInfo {
	var ops []opInfo

	for _, path := range paths.keys {
		item := paths.vals[path]

		type methodOp struct {
			verb string
			op   *Operation
		}
		// Iterate methods in a stable order.
		for _, mo := range []methodOp{
			{"GET", item.Get},
			{"POST", item.Post},
			{"PUT", item.Put},
			{"PATCH", item.Patch},
			{"DELETE", item.Delete},
		} {
			if mo.op == nil {
				continue
			}
			op := mo.op
			if skipOperations[op.OperationID] {
				continue
			}
			if len(op.Tags) == 0 {
				continue
			}
			tag := op.Tags[0]
			if _, ok := tagFile[tag]; !ok {
				continue
			}

			allParams := extractPathParams(path)
			recv, restParams := resourceScope(path, allParams)
			if forceClientReceiver[op.OperationID] {
				recv = ""
				restParams = allParams
			}
			info := opInfo{
				operationID: op.OperationID,
				httpMethod:  mo.verb,
				path:        path,
				summary:     op.Summary,
				tag:         tag,
				pathParams:  restParams,
				receiver:    recv,
			}

			// Request body type.
			if override, ok := requestTypeOverrides[op.OperationID]; ok {
				info.reqType = override
			} else if op.RequestBody != nil {
				if media, ok := op.RequestBody.Content["application/json"]; ok && media.Schema != nil {
					if media.Schema.Ref != "" {
						info.reqType = goTypeName(deref(media.Schema.Ref))
					}
				}
			}

			// Response type.
			if override, ok := responseTypeOverrides[op.OperationID]; ok {
				info.respType = override
			} else {
				// Check 200 then 201 response.
				for _, code := range []string{"200", "201"} {
					resp, ok := op.Responses[code]
					if !ok || resp.Content == nil {
						continue
					}
					media, ok := resp.Content["application/json"]
					if !ok || media.Schema == nil {
						continue
					}
					s := media.Schema
					if s.Type == "array" && s.Items != nil && s.Items.Ref != "" {
						info.respType = goTypeName(deref(s.Items.Ref))
						info.respSlice = true
					} else if s.Ref != "" {
						info.respType = goTypeName(deref(s.Ref))
					} else {
						info.respType = "StatusResponse"
					}
					break
				}
				if info.respType == "" {
					info.respType = "StatusResponse"
				}
			}

			ops = append(ops, info)
		}
	}
	return ops
}

// goMethodName returns the Go method name for an operation.
func goMethodName(opID string) string {
	if name, ok := methodNameOverrides[opID]; ok {
		return name
	}
	return toCamel(opID)
}

// genClientFile generates a Go source file with methods for ops. Each op
// becomes either a method on *Leg, *Room, or *Client depending on its path
// scope (see resourceScope). Methods returning *Leg / *Room / []Leg / []Room
// also populate the unexported client back-reference on the result so the
// returned object can be used to make further API calls.
func genClientFile(ops []opInfo) []byte {
	var b bytes.Buffer
	b.WriteString(generatedHeader)
	b.WriteString("package voiceblender\n\n")
	b.WriteString("import (\n")
	b.WriteString("\t\"context\"\n")
	b.WriteString("\t\"net/http\"\n")
	b.WriteString(")\n\n")

	for _, op := range ops {
		methodName := goMethodName(op.operationID)

		// Receiver-specific shorthand: which Go expression replaces {id} in the
		// path, what the receiver/client expression is, and the function header.
		var (
			recvVar    string
			clientExpr string
			pathSubs   map[string]string
			receiver   string
		)
		switch op.receiver {
		case "Leg":
			recvVar = "l"
			clientExpr = "l.client"
			pathSubs = map[string]string{"id": "l.ID"}
			receiver = "(l *Leg)"
		case "Room":
			recvVar = "r"
			clientExpr = "r.client"
			pathSubs = map[string]string{"id": "r.ID"}
			receiver = "(r *Room)"
		default:
			recvVar = "c"
			clientExpr = "c"
			pathSubs = nil
			receiver = "(c *Client)"
		}
		_ = recvVar

		// Build godoc comment.
		if op.summary != "" {
			fmt.Fprintf(&b, "// %s %s\n", methodName, strings.ToLower(op.summary[:1])+op.summary[1:])
		}

		// Build function signature.
		var sigParams []string
		sigParams = append(sigParams, "ctx context.Context")
		for _, p := range op.pathParams {
			sigParams = append(sigParams, p+" string")
		}
		if op.reqType != "" {
			sigParams = append(sigParams, "req "+op.reqType)
		}

		var retType string
		if op.respSlice {
			retType = "[]" + op.respType
		} else {
			retType = "*" + op.respType
		}

		fmt.Fprintf(&b, "func %s %s(%s) (%s, error) {\n",
			receiver, methodName, strings.Join(sigParams, ", "), retType)

		// Body encoding.
		if op.reqType != "" {
			b.WriteString("\tbody, err := encodeJSON(req)\n")
			b.WriteString("\tif err != nil {\n")
			b.WriteString("\t\treturn nil, err\n")
			b.WriteString("\t}\n")
		}

		// Variable declaration.
		if op.respSlice {
			fmt.Fprintf(&b, "\tvar out []%s\n", op.respType)
		} else {
			fmt.Fprintf(&b, "\tvar out %s\n", op.respType)
		}

		// Body args + return.
		goPath := buildGoPath(op.path, pathSubs)
		bodyArg := "nil"
		if op.reqType != "" {
			bodyArg = "body"
		}
		mc := httpMethodConst[op.httpMethod]
		needsClientBackref := op.respType == "Leg" || op.respType == "Room"

		if needsClientBackref {
			// Two-line form so we can populate out.client / out[i].client between
			// the HTTP call and the return.
			fmt.Fprintf(&b, "\tif err := %s.do(ctx, %s, %s, %s, &out); err != nil {\n",
				clientExpr, mc, goPath, bodyArg)
			if op.respSlice {
				b.WriteString("\t\treturn nil, err\n")
			} else {
				b.WriteString("\t\treturn nil, err\n")
			}
			b.WriteString("\t}\n")
			if op.respSlice {
				// Use the client value used for the call (clientExpr is "c", "l.client",
				// or "r.client" — all valid expressions for a *Client).
				fmt.Fprintf(&b, "\tfor i := range out {\n\t\tout[i].client = %s\n\t}\n", clientExpr)
				b.WriteString("\treturn out, nil\n")
			} else {
				fmt.Fprintf(&b, "\tout.client = %s\n", clientExpr)
				b.WriteString("\treturn &out, nil\n")
			}
		} else if op.respSlice {
			fmt.Fprintf(&b, "\treturn out, %s.do(ctx, %s, %s, %s, &out)\n", clientExpr, mc, goPath, bodyArg)
		} else {
			fmt.Fprintf(&b, "\treturn &out, %s.do(ctx, %s, %s, %s, &out)\n", clientExpr, mc, goPath, bodyArg)
		}

		b.WriteString("}\n\n")
	}

	return fmtGo(b.Bytes())
}

// ── AsyncAPI / VSI generation ─────────────────────────────────────────────────
//
// genVSI emits a generated file with:
//   - Go structs for every payload/result schema in asyncapi components.schemas.
//   - One method per `recv_*` operation, attached to *EventStream, that calls
//     the hand-written EventStream.call() helper to perform the round-trip.
//
// Cross-file refs (e.g. openapi.yaml#/components/schemas/LegView) are resolved
// against the openapi schemas we already parsed; if the referenced type does
// not exist there, the field/return falls back to json.RawMessage so output
// still compiles.

const vsiFileHeader = "// Code generated by cmd/generate from asyncapi.yaml. DO NOT EDIT.\n\n"

// schemaGoType renders a JSON Schema as a Go type, handling refs (same-file
// or cross-file), arrays, and primitive scalars.
func schemaGoType(s *Schema, asyncSchemas, openSchemas map[string]*Schema) string {
	if s == nil {
		return "json.RawMessage"
	}
	if s.Ref != "" {
		return resolveRefType(s.Ref, asyncSchemas, openSchemas)
	}
	switch s.Type {
	case "array":
		if s.Items != nil {
			return "[]" + schemaGoType(s.Items, asyncSchemas, openSchemas)
		}
		return "[]json.RawMessage"
	case "string":
		return "string"
	case "integer":
		return "int"
	case "boolean":
		return "bool"
	case "number":
		return "float64"
	case "object":
		return "map[string]interface{}"
	}
	return "json.RawMessage"
}

// resolveRefType resolves a $ref to a Go type name. Cross-file refs whose
// target schema isn't present in openSchemas degrade to json.RawMessage so
// generation isn't blocked by a missing dependency.
func resolveRefType(ref string, asyncSchemas, openSchemas map[string]*Schema) string {
	hash := strings.Index(ref, "#")
	if hash < 0 {
		return "json.RawMessage"
	}
	tail := ref[hash+1:]
	if strings.HasPrefix(tail, "/") {
		tail = tail[1:]
	}
	parts := strings.Split(tail, "/")
	name := parts[len(parts)-1]
	if name == "" {
		return "json.RawMessage"
	}
	crossFile := hash > 0
	if crossFile {
		// AsyncAPI refers to OpenAPI types by their final Go name, which may
		// differ from the spec name (RoomCreateRequest → CreateRoomRequest).
		// Accept either form.
		if _, ok := openSchemas[name]; ok {
			return goTypeName(name)
		}
		for orig, renamed := range typeRenames {
			if renamed == name {
				if _, ok := openSchemas[orig]; ok {
					return renamed
				}
			}
		}
		return "json.RawMessage"
	}
	if _, ok := asyncSchemas[name]; ok {
		return goTypeName(name)
	}
	if _, ok := openSchemas[name]; ok {
		return goTypeName(name)
	}
	return goTypeName(name)
}

// frameField returns the Go type for a wire-frame field (`payload` for
// command requests, `data` for `<cmd>.result` responses), and whether it
// exists. Missing means the command takes no input or returns no body. A
// schema with `type: "null"` is also treated as absent — that's how the
// AsyncAPI generator marks "this command has no payload".
func frameField(msg *aaMessage, fieldName string, asyncSchemas, openSchemas map[string]*Schema) (string, bool) {
	if msg == nil || msg.Payload == nil {
		return "", false
	}
	s := msg.Payload.Properties.vals[fieldName]
	if s == nil || s.Type == "null" {
		return "", false
	}
	return schemaGoType(s, asyncSchemas, openSchemas), true
}

// resolveOpMessage takes a $ref from an operation (request or reply) and
// returns the corresponding *aaMessage. Op message refs point at the channel
// (e.g. #/channels/vsi/messages/list_legs.result) where each entry is itself
// a $ref into components.messages. Returns nil if either hop fails.
func resolveOpMessage(spec *asyncAPISpec, ref string) *aaMessage {
	tail, ok := refTail(ref)
	if !ok || spec == nil {
		return nil
	}
	// Try direct lookup in components.messages first (for refs that already
	// point there directly).
	if m, ok := spec.Components.Messages[tail]; ok {
		return m
	}
	// Otherwise, search every channel for a matching message entry and
	// follow its $ref into components.messages.
	for _, ch := range spec.Channels {
		entry, ok := ch.Messages[tail]
		if !ok || entry == nil {
			continue
		}
		inner, ok := refTail(entry.Ref)
		if !ok {
			continue
		}
		if m, ok := spec.Components.Messages[inner]; ok {
			return m
		}
	}
	return nil
}

// genVSI emits the vsi.go file given a parsed AsyncAPI spec and the openapi
// schemas (for cross-file ref resolution).
func genVSI(spec *asyncAPISpec, openSchemas map[string]*Schema) []byte {
	var b bytes.Buffer
	b.WriteString(vsiFileHeader)
	b.WriteString("package voiceblender\n\n")
	b.WriteString("import (\n")
	b.WriteString("\t\"context\"\n")
	b.WriteString("\t\"encoding/json\"\n")
	b.WriteString(")\n\n")

	asyncSchemas := spec.Components.Schemas

	// Schemas declared in both asyncapi and openapi: emitted once in
	// requests.go (or hardcoded there for ICECandidateInit) and skipped here
	// so vsi.go references the existing type instead of redeclaring it.
	// WebRTCOfferResult / WebRTCCandidatesResult are async-only as far as Go
	// emission goes — the openapi spec mentions them but no Go file emits
	// them from there, so they must still be emitted in vsi.go.
	vsiSkipSchemas := map[string]bool{
		"ICECandidateInit":   true,
		"WebRTCOfferRequest": true,
	}

	// 1. Emit Go structs for every payload / result schema in deterministic
	//    order. Sort by Go type name for stable output.
	type namedSchema struct {
		raw string
		s   *Schema
	}
	var namedSchemas []namedSchema
	for k, s := range asyncSchemas {
		namedSchemas = append(namedSchemas, namedSchema{k, s})
	}
	sort.Slice(namedSchemas, func(i, j int) bool {
		return goTypeName(namedSchemas[i].raw) < goTypeName(namedSchemas[j].raw)
	})
	b.WriteString("// ── VSI payload / result schemas ──────────────────────────────────────────\n\n")
	for _, ns := range namedSchemas {
		if vsiSkipSchemas[ns.raw] {
			continue
		}
		genStruct(&b, ns.raw, ns.s)
	}

	// 2. Emit one method per recv_* operation, in document order.
	b.WriteString("// ── VSI command methods on *EventStream ───────────────────────────────────\n\n")
	for _, opName := range spec.Operations.keys {
		op := spec.Operations.vals[opName]
		if op.Action != "receive" || !strings.HasPrefix(opName, "recv_") {
			continue
		}
		cmdType := strings.TrimPrefix(opName, "recv_")
		methodName := toCamel(cmdType)

		// Resolve the request message → its payload field type (if any).
		var reqMsg *aaMessage
		if len(op.Messages) > 0 {
			reqMsg = resolveOpMessage(spec, op.Messages[0].Ref)
		}
		payloadType, hasPayload := frameField(reqMsg, "payload", asyncSchemas, openSchemas)

		// Resolve the result message → its data field type (if any). A reply
		// has both `<cmd>.result` and `error`; we want the former.
		var resMsg *aaMessage
		if op.Reply != nil {
			for _, mr := range op.Reply.Messages {
				name, ok := refTail(mr.Ref)
				if !ok || name == "error" {
					continue
				}
				resMsg = resolveOpMessage(spec, mr.Ref)
				if resMsg != nil {
					break
				}
			}
		}
		dataType, hasData := frameField(resMsg, "data", asyncSchemas, openSchemas)

		// Build the godoc.
		summary := op.Summary
		if summary == "" && reqMsg != nil {
			summary = reqMsg.Title
		}
		if summary != "" {
			fmt.Fprintf(&b, "// %s %s\n", methodName, strings.ToLower(summary[:1])+summary[1:])
		}

		// Signature.
		params := []string{"ctx context.Context"}
		if hasPayload {
			params = append(params, "payload "+payloadType)
		}
		retType := "error"
		if hasData {
			retType = "(" + dataType + ", error)"
		}
		fmt.Fprintf(&b, "func (s *EventStream) %s(%s) %s {\n",
			methodName, strings.Join(params, ", "), retType)

		// Body.
		payloadArg := "nil"
		if hasPayload {
			payloadArg = "payload"
		}
		if hasData {
			fmt.Fprintf(&b, "\tvar out %s\n", dataType)
			fmt.Fprintf(&b, "\treturn out, s.call(ctx, %q, %s, &out)\n", cmdType, payloadArg)
		} else {
			fmt.Fprintf(&b, "\treturn s.call(ctx, %q, %s, nil)\n", cmdType, payloadArg)
		}
		b.WriteString("}\n\n")
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
	asyncapi := flag.String("asyncapi", "", "path to asyncapi.yaml (optional; enables vsi.go generation)")
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

	// Parse asyncapi.yaml up front (if provided) so its schema names are known
	// before generating the type files. genModels suppresses placeholder
	// aliases for names defined here, since genVSI emits a real struct for them.
	var aaSpec *asyncAPISpec
	asyncDefined := map[string]bool{}
	if *asyncapi != "" {
		raw, err := os.ReadFile(*asyncapi)
		if err != nil {
			log.Fatalf("read %s: %v", *asyncapi, err)
		}
		var s asyncAPISpec
		if err := yaml.Unmarshal(raw, &s); err != nil {
			log.Fatalf("parse asyncapi.yaml: %v", err)
		}
		aaSpec = &s
		for name := range aaSpec.Components.Schemas {
			asyncDefined[name] = true
		}
	}

	// Generate type files.
	write(filepath.Join(*out, "models.go"), genModels(schemas, asyncDefined))
	write(filepath.Join(*out, "requests.go"), genRequests(schemas))
	write(filepath.Join(*out, "responses.go"), genResponses(schemas))
	if evData := genEvents(spec.XWebhooks); evData != nil {
		write(filepath.Join(*out, "events.go"), evData)
	}

	// Generate client method files from paths.
	allOps := extractOps(spec.Paths)

	// Group by tag → file.
	grouped := make(map[string][]opInfo)
	for _, op := range allOps {
		grouped[op.tag] = append(grouped[op.tag], op)
	}
	for tag, file := range tagFile {
		ops, ok := grouped[tag]
		if !ok {
			continue
		}
		write(filepath.Join(*out, file), genClientFile(ops))
	}

	// Generate VSI command file from asyncapi.yaml.
	if aaSpec != nil {
		write(filepath.Join(*out, "vsi.go"), genVSI(aaSpec, schemas))
	}
}
