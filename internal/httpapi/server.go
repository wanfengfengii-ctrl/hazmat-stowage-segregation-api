// Package httpapi exposes the dangerous-goods pre-stowage adjudication
// service over HTTP.
package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"prestow/internal/stowage"
)

// maxBodyBytes caps the request body so a manifest cannot exhaust memory.
const maxBodyBytes = 1 << 20 // 1 MiB

// NewRouter builds the Gin engine with all routes of the service.
func NewRouter() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	r.POST("/api/v1/pre-stowage/validate", validateManifest)
	return r
}

// fieldError pinpoints one invalid field by its JSON path.
type fieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// validateResponse is the adjudication result for a well-formed manifest.
type validateResponse struct {
	Release      bool               `json:"release"`
	CheckedPairs int                `json:"checked_pairs"`
	Conflicts    []stowage.Conflict `json:"conflicts"`
}

// validateManifest handles POST /api/v1/pre-stowage/validate. Any invalid
// item rejects the whole manifest with HTTP 400; a well-formed manifest is
// adjudicated and answered with HTTP 200 whether it is released or blocked.
func validateManifest(c *gin.Context) {
	items, ferrs := parseManifest(c.Request.Body)
	if len(ferrs) > 0 {
		code := "validation_failed"
		if len(ferrs) == 1 && ferrs[0].Field == "(body)" {
			code = "invalid_json"
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": code, "details": ferrs})
		return
	}
	checked, conflicts := stowage.Adjudicate(items)
	c.JSON(http.StatusOK, validateResponse{
		Release:      len(conflicts) == 0,
		CheckedPairs: checked,
		Conflicts:    conflicts,
	})
}

// parseManifest decodes and validates the request body. It returns the
// validated items, or every field error found — the whole manifest is
// rejected when any single item is invalid.
func parseManifest(r io.Reader) ([]stowage.Item, []fieldError) {
	body, err := io.ReadAll(io.LimitReader(r, maxBodyBytes+1))
	if err != nil || len(body) > maxBodyBytes {
		return nil, []fieldError{{Field: "(body)", Message: "request body is unreadable or exceeds 1 MiB"}}
	}

	var req struct {
		Items json.RawMessage `json:"items"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&req); err != nil {
		return nil, []fieldError{{Field: "(body)", Message: "body must be a single JSON object: " + err.Error()}}
	}
	if dec.More() {
		return nil, []fieldError{{Field: "(body)", Message: "unexpected data after the JSON object"}}
	}
	if req.Items == nil {
		return nil, []fieldError{{Field: "items", Message: "is required and must be a non-empty array"}}
	}

	var rawItems []map[string]json.RawMessage
	if err := json.Unmarshal(req.Items, &rawItems); err != nil {
		return nil, []fieldError{{Field: "items", Message: "must be an array of objects"}}
	}
	if len(rawItems) == 0 {
		return nil, []fieldError{{Field: "items", Message: "must contain at least one item"}}
	}

	var errs []fieldError
	items := make([]stowage.Item, len(rawItems))
	seen := make(map[string]int) // cargo ID -> index of first occurrence
	for i, raw := range rawItems {
		prefix := fmt.Sprintf("items[%d]", i)

		id, ok := parseCargoID(raw, prefix, &errs)
		if ok {
			if first, dup := seen[id]; dup {
				errs = append(errs, fieldError{
					Field:   prefix + ".cargo_id",
					Message: fmt.Sprintf("duplicates items[%d].cargo_id (%q); cargo_id must be unique in the manifest", first, id),
				})
			} else {
				seen[id] = i
				items[i].CargoID = id
			}
		}
		if class, ok := parseHazardClass(raw, prefix, &errs); ok {
			items[i].HazardClass = class
		}
		if bay, ok := parseBay(raw, prefix, &errs); ok {
			items[i].Bay = bay
		}
		if deck, ok := parseDeck(raw, prefix, &errs); ok {
			items[i].Deck = deck
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return items, nil
}

// parseCargoID validates the non-empty, manifest-unique cargo ID. Uniqueness
// across items is enforced by the caller.
func parseCargoID(raw map[string]json.RawMessage, prefix string, errs *[]fieldError) (string, bool) {
	field := prefix + ".cargo_id"
	v, present := raw["cargo_id"]
	if !present {
		*errs = append(*errs, fieldError{Field: field, Message: "is required"})
		return "", false
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		*errs = append(*errs, fieldError{Field: field, Message: "must be a string"})
		return "", false
	}
	s = strings.TrimSpace(s)
	if s == "" {
		*errs = append(*errs, fieldError{Field: field, Message: "must not be empty"})
		return "", false
	}
	return s, true
}

// allowedClasses is the set of hazard classes accepted by the terminal.
var allowedClasses = map[string]bool{
	"1": true, "2.1": true, "3": true, "4.1": true, "5.1": true, "8": true,
}

// classByNumber maps the numeric JSON form of a hazard class to its
// canonical string form, so both "2.1" and 2.1 are accepted.
var classByNumber = map[float64]string{
	1: "1", 2.1: "2.1", 3: "3", 4.1: "4.1", 5.1: "5.1", 8: "8",
}

const hazardClassMessage = `must be one of "1", "2.1", "3", "4.1", "5.1", "8"`

// parseHazardClass validates the hazard class, given either as a JSON string
// ("2.1") or as a JSON number (2.1), and returns its canonical string form.
func parseHazardClass(raw map[string]json.RawMessage, prefix string, errs *[]fieldError) (string, bool) {
	field := prefix + ".hazard_class"
	v, present := raw["hazard_class"]
	if !present {
		*errs = append(*errs, fieldError{Field: field, Message: "is required"})
		return "", false
	}
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		if allowedClasses[s] {
			return s, true
		}
		*errs = append(*errs, fieldError{Field: field, Message: hazardClassMessage})
		return "", false
	}
	var n json.Number
	if err := json.Unmarshal(v, &n); err == nil {
		if f, ferr := n.Float64(); ferr == nil {
			if canonical, ok := classByNumber[f]; ok {
				return canonical, true
			}
		}
	}
	*errs = append(*errs, fieldError{Field: field, Message: hazardClassMessage})
	return "", false
}

// parseBay validates the bay as a JSON integer in the inclusive range 1..30.
func parseBay(raw map[string]json.RawMessage, prefix string, errs *[]fieldError) (int, bool) {
	field := prefix + ".bay"
	v, present := raw["bay"]
	if !present {
		*errs = append(*errs, fieldError{Field: field, Message: "is required"})
		return 0, false
	}
	const msg = "must be an integer between 1 and 30"
	// json.Number also accepts JSON strings, so reject any literal that does
	// not start like a JSON number before decoding.
	trimmed := bytes.TrimSpace(v)
	if len(trimmed) == 0 || (trimmed[0] != '-' && (trimmed[0] < '0' || trimmed[0] > '9')) {
		*errs = append(*errs, fieldError{Field: field, Message: msg})
		return 0, false
	}
	var n json.Number
	if err := json.Unmarshal(v, &n); err != nil {
		*errs = append(*errs, fieldError{Field: field, Message: msg})
		return 0, false
	}
	bay, err := strconv.ParseInt(n.String(), 10, 64)
	if err != nil || bay < 1 || bay > 30 {
		*errs = append(*errs, fieldError{Field: field, Message: msg})
		return 0, false
	}
	return int(bay), true
}

// parseDeck validates the deck as exactly "U" (upper) or "L" (lower).
func parseDeck(raw map[string]json.RawMessage, prefix string, errs *[]fieldError) (string, bool) {
	field := prefix + ".deck"
	v, present := raw["deck"]
	if !present {
		*errs = append(*errs, fieldError{Field: field, Message: "is required"})
		return "", false
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil || (s != "U" && s != "L") {
		*errs = append(*errs, fieldError{Field: field, Message: `must be "U" or "L"`})
		return "", false
	}
	return s, true
}
