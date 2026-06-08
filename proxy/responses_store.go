package proxy

// On-disk persistence for the OpenAI Responses API. Each response is stored as
// a JSON file under <config-dir>/responses, keyed by a sanitized response ID,
// so that a follow-up request carrying previous_response_id can reconstruct the
// prior turn. Files older than responsesDefaultTTL are purged.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"kiro-go/config"
	"kiro-go/logger"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	responsesDirName    = "responses"
	responsesDefaultTTL = 30 * 24 * time.Hour
)

// responsesDir returns the directory where stored responses live.
func responsesDir() string {
	return filepath.Join(config.GetConfigDir(), responsesDirName)
}

// generateResponseID returns a unique, URL-safe response ID (resp_<random><time>).
func generateResponseID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("resp_%d%012x", time.Now().UnixNano(), 0)
	}
	return "resp_" + hex.EncodeToString(buf) + fmt.Sprintf("%08x", time.Now().Unix()&0xffffffff)
}

// generateOutputItemID returns a unique ID for an output item, namespaced by
// the given prefix (e.g. "msg", "fc").
func generateOutputItemID(prefix string) string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(buf)
}

// saveResponse persists resp to disk atomically (write to .tmp then rename),
// stamping StoredAt if unset. The file is written 0600 since it may contain
// conversation content.
func saveResponse(resp *ResponsesObject) error {
	if resp == nil || resp.ID == "" {
		return fmt.Errorf("response missing id")
	}
	dir := responsesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create responses dir: %w", err)
	}
	if resp.StoredAt == 0 {
		resp.StoredAt = time.Now().Unix()
	}

	persisted := storedResponseDoc{
		ID:                 resp.ID,
		Object:             resp.Object,
		CreatedAt:          resp.CreatedAt,
		Status:             resp.Status,
		Model:              resp.Model,
		Output:             resp.Output,
		Usage:              resp.Usage,
		PreviousResponseID: resp.PreviousResponseID,
		Metadata:           resp.Metadata,
		Instructions:       resp.Instructions,
		StoredInput:        resp.StoredInput,
		StoredAt:           resp.StoredAt,
	}

	path := filepath.Join(dir, sanitizeResponseID(resp.ID)+".json")
	data, err := json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal stored response: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write stored response: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("commit stored response: %w", err)
	}
	return nil
}

// loadResponse reads a stored response by ID. If the file is older than
// responsesDefaultTTL it is deleted and an error is returned.
func loadResponse(id string) (*ResponsesObject, error) {
	if id == "" {
		return nil, fmt.Errorf("empty response id")
	}
	path := filepath.Join(responsesDir(), sanitizeResponseID(id)+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc storedResponseDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode stored response: %w", err)
	}
	if doc.StoredAt > 0 && time.Since(time.Unix(doc.StoredAt, 0)) > responsesDefaultTTL {
		_ = os.Remove(path)
		return nil, fmt.Errorf("stored response expired")
	}
	return &ResponsesObject{
		ID:                 doc.ID,
		Object:             doc.Object,
		CreatedAt:          doc.CreatedAt,
		Status:             doc.Status,
		Model:              doc.Model,
		Output:             doc.Output,
		Usage:              doc.Usage,
		PreviousResponseID: doc.PreviousResponseID,
		Metadata:           doc.Metadata,
		Instructions:       doc.Instructions,
		StoredInput:        doc.StoredInput,
		StoredAt:           doc.StoredAt,
	}, nil
}

// purgeExpiredResponses removes stored-response files whose mtime is older than
// ttl (falling back to responsesDefaultTTL when ttl <= 0). Called periodically
// by the background goroutine.
func purgeExpiredResponses(ttl time.Duration) {
	if ttl <= 0 {
		ttl = responsesDefaultTTL
	}
	dir := responsesDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-ttl)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		full := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(full); err != nil {
				logger.Warnf("[Responses] purge %s failed: %v", e.Name(), err)
			}
		}
	}
}

func logResponsesPersistFailure(id string, err error) {
	logger.Warnf("[Responses] persist %s failed: %v", id, err)
}

// sanitizeResponseID strips any character outside [A-Za-z0-9_-] so the ID is
// safe to use as a filename and cannot escape the responses directory.
func sanitizeResponseID(id string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '_' || r == '-':
			return r
		default:
			return -1
		}
	}, id)
	if cleaned == "" {
		return "invalid"
	}
	return cleaned
}

// storedResponseDoc is the on-disk JSON shape. It differs from ResponsesObject
// in that StoredInput is serialized (so the original request can be replayed).
type storedResponseDoc struct {
	ID                 string               `json:"id"`
	Object             string               `json:"object"`
	CreatedAt          int64                `json:"created_at"`
	Status             string               `json:"status"`
	Model              string               `json:"model"`
	Output             []ResponseOutputItem `json:"output"`
	Usage              ResponsesUsage       `json:"usage"`
	PreviousResponseID string               `json:"previous_response_id,omitempty"`
	Metadata           map[string]string    `json:"metadata,omitempty"`
	Instructions       string               `json:"instructions,omitempty"`
	StoredInput        json.RawMessage      `json:"stored_input,omitempty"`
	StoredAt           int64                `json:"stored_at"`
}
