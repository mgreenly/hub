package mcp

import (
	"encoding/json"
	"errors"
	"net/http"
)

// toolDescriptors returns the notify_* tool set. For the skeleton this is just
// notify_whoami; real notify tools are added to this list and to dispatchTool
// as the domain is built out. Schemas are hand-coded; a full JSON Schema isn't
// required by MCP clients but improves the LLM hinting.
func toolDescriptors() []map[string]any {
	return []map[string]any{
		desc("notify_whoami", "Return the authenticated caller's identity (owner email and client id) as established by the platform's auth gate. Takes no inputs; the end-to-end auth proof.", obj(map[string]any{})),
	}
}

func desc(name, description string, schema map[string]any) map[string]any {
	return map[string]any{"name": name, "description": description, "inputSchema": schema}
}

func obj(props map[string]any, required ...string) map[string]any {
	o := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}

func typ(t string) map[string]any { return map[string]any{"type": t} }

// ── dispatch ──────────────────────────────────────────────────────────────

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (h *Handler) handleToolCall(w http.ResponseWriter, req jsonRPCRequest, id Identity) {
	var p toolCallParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		writeJSONRPCError(w, req.ID, -32602, "invalid params")
		return
	}
	res, err := dispatchTool(p.Name, id)
	if err != nil {
		writeJSONRPCResult(w, req.ID, toolResultErr(err.Error()))
		return
	}
	writeJSONRPCResult(w, req.ID, res)
}

func dispatchTool(name string, id Identity) (map[string]any, error) {
	switch name {
	case "notify_whoami":
		return toolWhoami(id)
	default:
		return nil, errors.New("unknown tool: " + name)
	}
}

// ── tool implementations ─────────────────────────────────────────────────

func toolWhoami(id Identity) (map[string]any, error) {
	return toolResultJSON(map[string]any{
		"owner_email": id.OwnerEmail,
		"client_id":   id.ClientID,
	})
}

// ── shared helpers ──────────────────────────────────────────────────────

func toolResultJSON(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return toolResultText(string(b)), nil
}
