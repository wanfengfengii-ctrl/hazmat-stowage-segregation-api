// Package httpapi exposes the dangerous-goods pre-stowage adjudication
// service over HTTP.
package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
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
	r.POST("/api/v1/pre-stowage/relocation-preview", relocationPreview)
	r.POST("/api/v1/pre-stowage/loading-waves", loadingWaves)
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

// relocationPreviewResponse is the result of rehearsing the move of one
// container: the adjudication of the manifest as submitted (before) and with
// the target container placed at the candidate position (after), plus the
// conflicts the move would resolve and introduce.
type relocationPreviewResponse struct {
	Before              validateResponse   `json:"before"`
	After               validateResponse   `json:"after"`
	ResolvedConflicts   []stowage.Conflict `json:"resolved_conflicts"`
	IntroducedConflicts []stowage.Conflict `json:"introduced_conflicts"`
}

// loadingWavesResponse is the adjudication result of a well-formed manifest
// together with its partition into loading waves.
type loadingWavesResponse struct {
	validateResponse
	WaveCount int            `json:"wave_count"`
	Waves     []stowage.Wave `json:"waves"`
}

// validateManifest handles POST /api/v1/pre-stowage/validate. Any invalid
// item rejects the whole manifest with HTTP 400; a well-formed manifest is
// adjudicated and answered with HTTP 200 whether it is released or blocked.
func validateManifest(c *gin.Context) {
	items, ferrs := parseManifest(c.Request.Body)
	if len(ferrs) > 0 {
		rejectValidation(c, ferrs)
		return
	}
	c.JSON(http.StatusOK, adjudicateManifest(items))
}

// relocationPreview handles POST /api/v1/pre-stowage/relocation-preview. The
// manifest, the target cargo ID and the candidate position are all validated
// up front — any error rejects the whole request with HTTP 400 and no
// partial result. Otherwise the manifest is adjudicated as submitted and
// once more with the target container moved to the candidate position.
func relocationPreview(c *gin.Context) {
	items, target, cand, ferrs := parseRelocationPreview(c.Request.Body)
	if len(ferrs) > 0 {
		rejectValidation(c, ferrs)
		return
	}

	before := adjudicateManifest(items)
	moved := make([]stowage.Item, len(items))
	copy(moved, items)
	for i := range moved {
		if moved[i].CargoID == target {
			moved[i].Bay = cand.Bay
			moved[i].Deck = cand.Deck
		}
	}
	after := adjudicateManifest(moved)
	resolved, introduced := stowage.DiffConflicts(before.Conflicts, after.Conflicts)

	c.JSON(http.StatusOK, relocationPreviewResponse{
		Before:              before,
		After:               after,
		ResolvedConflicts:   resolved,
		IntroducedConflicts: introduced,
	})
}

// loadingWaves handles POST /api/v1/pre-stowage/loading-waves. The manifest
// is validated exactly as in the validate endpoint — any error rejects the
// whole request with HTTP 400 and no partial result — then adjudicated and
// partitioned into loading waves so that no wave holds two conflicting
// containers.
func loadingWaves(c *gin.Context) {
	items, ferrs := parseLoadingWaves(c.Request.Body)
	if len(ferrs) > 0 {
		rejectValidation(c, ferrs)
		return
	}
	waves := stowage.LoadingWaves(items)
	c.JSON(http.StatusOK, loadingWavesResponse{
		validateResponse: adjudicateManifest(items),
		WaveCount:        len(waves),
		Waves:            waves,
	})
}

// adjudicateManifest runs the segregation rules over a well-formed manifest
// and shapes the response body shared by both endpoints.
func adjudicateManifest(items []stowage.Item) validateResponse {
	checked, conflicts := stowage.Adjudicate(items)
	return validateResponse{
		Release:      len(conflicts) == 0,
		CheckedPairs: checked,
		Conflicts:    conflicts,
	}
}

// rejectValidation answers HTTP 400 with every field error found.
func rejectValidation(c *gin.Context, ferrs []fieldError) {
	code := "validation_failed"
	if len(ferrs) == 1 && ferrs[0].Field == "(body)" {
		code = "invalid_json"
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": code, "details": ferrs})
}

// parseManifest decodes and validates the request body. It returns the
// validated items, or every field error found — the whole manifest is
// rejected when any single item is invalid.
func parseManifest(r io.Reader) ([]stowage.Item, []fieldError) {
	body, ferrs := readBody(r)
	if ferrs != nil {
		return nil, ferrs
	}
	var req struct {
		Items json.RawMessage `json:"items"`
	}
	if ferrs := decodeObject(body, &req); ferrs != nil {
		return nil, ferrs
	}
	return parseItems(req.Items)
}

// parseLoadingWaves decodes and validates the request body of the
// loading-waves endpoint. The body accepts only the items field, validated
// exactly as in parseManifest; any other top-level field is reported by its
// name, ahead of the item errors, and the whole manifest is rejected when
// any error is found.
func parseLoadingWaves(r io.Reader) ([]stowage.Item, []fieldError) {
	body, ferrs := readBody(r)
	if ferrs != nil {
		return nil, ferrs
	}
	var fields map[string]json.RawMessage
	if ferrs := decodeObject(body, &fields); ferrs != nil {
		return nil, ferrs
	}
	var errs []fieldError
	for _, name := range sortedKeys(fields) {
		if name != "items" {
			errs = append(errs, fieldError{
				Field:   name,
				Message: "is not allowed; the request accepts only items",
			})
		}
	}
	items, itemErrs := parseItems(fields["items"])
	errs = append(errs, itemErrs...)
	if len(errs) > 0 {
		return nil, errs
	}
	return items, nil
}

// candidatePosition is the requested new slot for the target container of a
// relocation preview.
type candidatePosition struct {
	Bay  int
	Deck string
}

// parseRelocationPreview decodes and validates the request body of the
// relocation-preview endpoint. The items are validated exactly as in
// parseManifest; on top of that the target cargo ID must name an item of the
// manifest and the candidate position must hold a legal bay and deck. Every
// error found is reported at once and no partial result is produced.
func parseRelocationPreview(r io.Reader) (items []stowage.Item, target string, cand candidatePosition, ferrs []fieldError) {
	body, ferrs := readBody(r)
	if ferrs != nil {
		return nil, "", candidatePosition{}, ferrs
	}
	var req struct {
		Items         json.RawMessage `json:"items"`
		TargetCargoID json.RawMessage `json:"target_cargo_id"`
		Candidate     json.RawMessage `json:"candidate"`
	}
	if ferrs := decodeObject(body, &req); ferrs != nil {
		return nil, "", candidatePosition{}, ferrs
	}

	var errs []fieldError
	items, itemErrs := parseItems(req.Items)
	errs = append(errs, itemErrs...)
	target, targetOK := parseTargetCargoID(req.TargetCargoID, &errs)
	cand, _ = parseCandidate(req.Candidate, &errs)

	// The existence check needs a valid manifest and a parsed target ID;
	// otherwise the errors collected so far already report the real problem.
	if targetOK && len(itemErrs) == 0 && !cargoIDExists(items, target) {
		errs = append(errs, fieldError{
			Field:   "target_cargo_id",
			Message: fmt.Sprintf("no item with cargo_id %q in the manifest", target),
		})
	}
	if len(errs) > 0 {
		return nil, "", candidatePosition{}, errs
	}
	return items, target, cand, nil
}

// cargoIDExists reports whether some item carries the given cargo ID.
func cargoIDExists(items []stowage.Item, id string) bool {
	for _, it := range items {
		if it.CargoID == id {
			return true
		}
	}
	return false
}

// parseTargetCargoID validates the cargo ID of the container to move.
func parseTargetCargoID(raw json.RawMessage, errs *[]fieldError) (string, bool) {
	const field = "target_cargo_id"
	if raw == nil {
		*errs = append(*errs, fieldError{Field: field, Message: "is required"})
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
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

// parseCandidate validates the candidate position as an object whose bay and
// deck obey exactly the same rules as the corresponding item fields. Any
// other field is rejected as unknown.
func parseCandidate(raw json.RawMessage, errs *[]fieldError) (candidatePosition, bool) {
	var cand candidatePosition
	if raw == nil {
		*errs = append(*errs, fieldError{Field: "candidate", Message: "is required and must be an object with bay and deck"})
		return cand, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		*errs = append(*errs, fieldError{Field: "candidate", Message: "must be an object with bay and deck"})
		return cand, false
	}
	ok := true
	for _, name := range sortedKeys(fields) {
		if name != "bay" && name != "deck" {
			*errs = append(*errs, fieldError{
				Field:   "candidate." + name,
				Message: "is not allowed; candidate accepts only bay and deck",
			})
			ok = false
		}
	}
	if bay, bayOK := parseBay(fields, "candidate", errs); bayOK {
		cand.Bay = bay
	} else {
		ok = false
	}
	if deck, deckOK := parseDeck(fields, "candidate", errs); deckOK {
		cand.Deck = deck
	} else {
		ok = false
	}
	return cand, ok
}

// sortedKeys returns the map keys in ascending order so that the reported
// field errors are deterministic.
func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// readBody reads the request body up to the size cap.
func readBody(r io.Reader) ([]byte, []fieldError) {
	body, err := io.ReadAll(io.LimitReader(r, maxBodyBytes+1))
	if err != nil || len(body) > maxBodyBytes {
		return nil, []fieldError{{Field: "(body)", Message: "request body is unreadable or exceeds 1 MiB"}}
	}
	return body, nil
}

// decodeObject decodes the body as a single JSON object into req.
func decodeObject(body []byte, req any) []fieldError {
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(req); err != nil {
		return []fieldError{{Field: "(body)", Message: "body must be a single JSON object: " + err.Error()}}
	}
	if dec.More() {
		return []fieldError{{Field: "(body)", Message: "unexpected data after the JSON object"}}
	}
	return nil
}

// parseItems validates the items array shared by both endpoints. It returns
// the validated items, or every field error found — the whole manifest is
// rejected when any single item is invalid.
func parseItems(raw json.RawMessage) ([]stowage.Item, []fieldError) {
	if raw == nil {
		return nil, []fieldError{{Field: "items", Message: "is required and must be a non-empty array"}}
	}

	var rawItems []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawItems); err != nil {
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
