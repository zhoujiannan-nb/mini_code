package provider

// =====================================================================
// TEMP-DEBUG (DELETE ME): request body dump for prefix-cache debugging.
//
// Writes the complete JSON body of every LLM request (model, messages,
// temperature, max_tokens, stream, tools, tool_choice) to
// ~/.mini_code/llm_request_latest.txt, rotating the previous file into
// ~/.mini_code/llm_request_previous.txt. The body is dumped right before
// the HTTP request is created, so a request that gets canceled (user
// clicks stop) is still captured.
//
// TO REMOVE: delete this file plus the one-line call to
// tempDumpRequestBody in doRequest (provider/client.go, marked with the
// same TEMP-DEBUG tag).
// =====================================================================

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/user/mini_code/config"
)

var tempDumpMu sync.Mutex

// tempDumpRequestBody rotates the two dump files and writes the current
// request body (raw bytes, exactly what is sent) as the new latest.
func tempDumpRequestBody(body []byte) {
	tempDumpMu.Lock()
	defer tempDumpMu.Unlock()
	dir, err := config.GetConfigDir()
	if err != nil {
		return
	}
	latest := filepath.Join(dir, "llm_request_latest.txt")
	previous := filepath.Join(dir, "llm_request_previous.txt")
	if data, err := os.ReadFile(latest); err == nil {
		_ = os.WriteFile(previous, data, 0o644)
	}
	_ = os.WriteFile(latest, body, 0o644)
}
