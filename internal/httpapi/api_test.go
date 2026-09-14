package httpapi_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"prestow/internal/httpapi"
)

// baseURL returns the API under test. When API_BASE_URL is set (as in the
// docker-compose "verify" service) the tests run against that live server;
// otherwise they spin up the real router on a local httptest listener. Either
// way the requests go over genuine HTTP using only the standard library.
func baseURL(t *testing.T) string {
	t.Helper()
	if u := os.Getenv("API_BASE_URL"); u != "" {
		u = strings.TrimRight(u, "/")
		waitForHealthy(t, u)
		return u
	}
	srv := httptest.NewServer(httpapi.NewRouter())
	t.Cleanup(srv.Close)
	return srv.URL
}

// waitForHealthy polls /healthz until the server answers or the deadline
// passes, so the compose "verify" service can start before the API is ready.
func waitForHealthy(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("API at %s did not become healthy", base)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// conflict mirrors one entry of the response's conflicts array.
type conflict struct {
	Pair   [2]string `json:"pair"`
	Rule   string    `json:"rule"`
	Reason string    `json:"reason"`
}

// adjudication mirrors the 200 response body.
type adjudication struct {
	Release      bool       `json:"release"`
	CheckedPairs int        `json:"checked_pairs"`
	Conflicts    []conflict `json:"conflicts"`
}

// previewResponse mirrors the relocation-preview 200 response body.
type previewResponse struct {
	Before              adjudication `json:"before"`
	After               adjudication `json:"after"`
	ResolvedConflicts   []conflict   `json:"resolved_conflicts"`
	IntroducedConflicts []conflict   `json:"introduced_conflicts"`
}

// validationFailure mirrors the 400 response body.
type validationFailure struct {
	Error   string `json:"error"`
	Details []struct {
		Field   string `json:"field"`
		Message string `json:"message"`
	} `json:"details"`
}

// Endpoint paths shared by the test helpers.
const (
	validatePath = "/api/v1/pre-stowage/validate"
	previewPath  = "/api/v1/pre-stowage/relocation-preview"
)

// postJSON POSTs a raw JSON body to one endpoint of the API.
func postJSON(t *testing.T, base, path, body string) (int, []byte) {
	t.Helper()
	resp, err := http.Post(base+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s failed: %v", path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response: %v", err)
	}
	return resp.StatusCode, data
}

// postManifest POSTs a raw JSON body to the validation endpoint.
func postManifest(t *testing.T, base, body string) (int, []byte) {
	return postJSON(t, base, validatePath, body)
}

// postPreview POSTs a raw JSON body to the relocation-preview endpoint.
func postPreview(t *testing.T, base, body string) (int, []byte) {
	return postJSON(t, base, previewPath, body)
}

// adjudicate posts a manifest and decodes the expected 200 response.
func adjudicate(t *testing.T, base, body string) adjudication {
	t.Helper()
	status, data := postManifest(t, base, body)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", status, data)
	}
	var out adjudication
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decoding response: %v; body: %s", err, data)
	}
	return out
}

// preview posts a relocation-preview request and decodes the 200 response.
func preview(t *testing.T, base, body string) previewResponse {
	t.Helper()
	status, data := postPreview(t, base, body)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", status, data)
	}
	var out previewResponse
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decoding response: %v; body: %s", err, data)
	}
	return out
}

// reject posts a manifest and decodes the expected 400 response.
func reject(t *testing.T, base, body string) validationFailure {
	t.Helper()
	status, data := postManifest(t, base, body)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", status, data)
	}
	var out validationFailure
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decoding response: %v; body: %s", err, data)
	}
	return out
}

// itemsJSON renders the items array shared by both request builders from
// "cargo_id,class,bay,deck" tuples.
func itemsJSON(items ...[4]string) string {
	var sb strings.Builder
	for i, it := range items {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"cargo_id":%q,"hazard_class":%q,"bay":%s,"deck":%q}`,
			it[0], it[1], it[2], it[3])
	}
	return sb.String()
}

// manifest builds a validate request body from "cargo_id,class,bay,deck"
// tuples.
func manifest(items ...[4]string) string {
	return `{"items":[` + itemsJSON(items...) + `]}`
}

// previewBody builds a relocation-preview request body from the manifest
// tuples, the target cargo ID and the candidate bay/deck.
func previewBody(items [][4]string, target string, bay int, deck string) string {
	return fmt.Sprintf(`{"items":[%s],"target_cargo_id":%q,"candidate":{"bay":%d,"deck":%q}}`,
		itemsJSON(items...), target, bay, deck)
}

// fields collects the field paths of a validation failure.
func fields(v validationFailure) []string {
	out := make([]string, len(v.Details))
	for i, d := range v.Details {
		out[i] = d.Field
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestHealthz(t *testing.T) {
	base := baseURL(t)
	resp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestReleaseWhenAllPairsCompatible(t *testing.T) {
	base := baseURL(t)
	out := adjudicate(t, base, manifest(
		[4]string{"C1", "3", "5", "U"},
		[4]string{"C2", "4.1", "9", "L"},
		[4]string{"C3", "2.1", "20", "U"},
	))
	if !out.Release {
		t.Fatalf("release = false, want true; conflicts: %+v", out.Conflicts)
	}
	if out.CheckedPairs != 3 {
		t.Fatalf("checked_pairs = %d, want 3", out.CheckedPairs)
	}
	if len(out.Conflicts) != 0 {
		t.Fatalf("conflicts = %+v, want empty", out.Conflicts)
	}
}

func TestClass1Isolation(t *testing.T) {
	base := baseURL(t)
	out := adjudicate(t, base, manifest(
		[4]string{"EXPL", "1", "1", "U"},
		[4]string{"ACID", "8", "2", "L"},
	))
	if out.Release {
		t.Fatal("release = true, want false")
	}
	if len(out.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want exactly 1", out.Conflicts)
	}
	c := out.Conflicts[0]
	if c.Pair != [2]string{"ACID", "EXPL"} {
		t.Fatalf("pair = %v, want [ACID EXPL] (ascending)", c.Pair)
	}
	if c.Rule != "CLASS_1_ISOLATION" {
		t.Fatalf("rule = %q, want CLASS_1_ISOLATION", c.Rule)
	}
	if c.Reason == "" {
		t.Fatal("reason must not be empty")
	}
}

func TestClass1PairIsCompatible(t *testing.T) {
	base := baseURL(t)
	out := adjudicate(t, base, manifest(
		[4]string{"A", "1", "1", "U"},
		[4]string{"B", "1", "2", "L"},
	))
	if !out.Release {
		t.Fatalf("two class-1 containers must be compatible; conflicts: %+v", out.Conflicts)
	}
}

// TestOxidizerBayBoundary pins the bay-difference boundary at exactly 2 for
// class 5.1 against classes 3 and 4.1.
func TestOxidizerBayBoundary(t *testing.T) {
	base := baseURL(t)
	cases := []struct {
		name        string
		other       string
		bayA, bayB  int
		wantRelease bool
	}{
		{"same_bay", "3", 5, 5, false},
		{"diff_1", "3", 5, 6, false},
		{"diff_2_released", "3", 5, 7, true},
		{"diff_1_class_4.1", "4.1", 29, 30, false},
		{"diff_2_class_4.1_released", "4.1", 28, 30, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := adjudicate(t, base, manifest(
				[4]string{"OXI", "5.1", fmt.Sprint(tc.bayA), "U"},
				[4]string{"FLAM", tc.other, fmt.Sprint(tc.bayB), "L"},
			))
			if out.Release != tc.wantRelease {
				t.Fatalf("release = %v, want %v; conflicts: %+v", out.Release, tc.wantRelease, out.Conflicts)
			}
			if !tc.wantRelease && out.Conflicts[0].Rule != "OXIDIZER_BAY_SEPARATION" {
				t.Fatalf("rule = %q, want OXIDIZER_BAY_SEPARATION", out.Conflicts[0].Rule)
			}
		})
	}
}

// TestCorrosiveFlammableGasBoundary pins the deck/bay conditions for a
// class 8 / class 2.1 pair, including the bay 1/30 edges.
func TestCorrosiveFlammableGasBoundary(t *testing.T) {
	base := baseURL(t)
	cases := []struct {
		name         string
		deckA, deckB string
		bayA, bayB   int
		wantRelease  bool
	}{
		{"same_deck_same_bay", "U", "U", 5, 5, false},
		{"same_deck_different_bay", "U", "U", 5, 9, false},
		{"different_deck_same_bay", "U", "L", 5, 5, false},
		{"different_deck_adjacent_bay_released", "U", "L", 5, 6, true},
		{"edge_bays_1_and_2_released", "U", "L", 1, 2, true},
		{"edge_bays_29_and_30_same_deck", "L", "L", 29, 30, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := adjudicate(t, base, manifest(
				[4]string{"ACID", "8", fmt.Sprint(tc.bayA), tc.deckA},
				[4]string{"GAS", "2.1", fmt.Sprint(tc.bayB), tc.deckB},
			))
			if out.Release != tc.wantRelease {
				t.Fatalf("release = %v, want %v; conflicts: %+v", out.Release, tc.wantRelease, out.Conflicts)
			}
			if !tc.wantRelease && out.Conflicts[0].Rule != "CORROSIVE_FLAMMABLE_GAS_SEPARATION" {
				t.Fatalf("rule = %q, want CORROSIVE_FLAMMABLE_GAS_SEPARATION", out.Conflicts[0].Rule)
			}
		})
	}
}

// TestDeterministicAndSortedConflicts submits a manifest with several
// conflicts in two different input orders and requires byte-identical,
// lexicographically sorted conflict lists.
func TestDeterministicAndSortedConflicts(t *testing.T) {
	base := baseURL(t)
	forward := manifest(
		[4]string{"DELTA", "1", "4", "U"},
		[4]string{"ALPHA", "5.1", "10", "U"},
		[4]string{"CHARLIE", "3", "11", "L"},
		[4]string{"BRAVO", "8", "20", "U"},
		[4]string{"ECHO", "2.1", "22", "U"},
	)
	reversed := manifest(
		[4]string{"ECHO", "2.1", "22", "U"},
		[4]string{"BRAVO", "8", "20", "U"},
		[4]string{"CHARLIE", "3", "11", "L"},
		[4]string{"ALPHA", "5.1", "10", "U"},
		[4]string{"DELTA", "1", "4", "U"},
	)

	status1, body1 := postManifest(t, base, forward)
	status2, body2 := postManifest(t, base, reversed)
	if status1 != http.StatusOK || status2 != http.StatusOK {
		t.Fatalf("statuses = %d, %d; want 200, 200", status1, status2)
	}
	if string(body1) != string(body2) {
		t.Fatalf("input order changed the response:\n%s\nvs\n%s", body1, body2)
	}

	var out adjudication
	if err := json.Unmarshal(body1, &out); err != nil {
		t.Fatal(err)
	}
	if out.Release {
		t.Fatal("release = true, want false")
	}
	if out.CheckedPairs != 10 {
		t.Fatalf("checked_pairs = %d, want 10", out.CheckedPairs)
	}
	// DELTA (class 1) conflicts with the other four; ALPHA/CHARLIE violate
	// the oxidizer separation; BRAVO/ECHO share the upper deck.
	wantPairs := [][2]string{
		{"ALPHA", "CHARLIE"},
		{"ALPHA", "DELTA"},
		{"BRAVO", "DELTA"},
		{"BRAVO", "ECHO"},
		{"CHARLIE", "DELTA"},
		{"DELTA", "ECHO"},
	}
	if len(out.Conflicts) != len(wantPairs) {
		t.Fatalf("conflicts = %+v, want %d entries", out.Conflicts, len(wantPairs))
	}
	for i, want := range wantPairs {
		if out.Conflicts[i].Pair != want {
			t.Fatalf("conflict %d = %v, want %v", i, out.Conflicts[i].Pair, want)
		}
	}
}

// TestWholeBatchRejectedOnAnyInvalidItem mixes valid and invalid items: the
// whole manifest must be rejected with a 400 and a precise field path.
func TestWholeBatchRejectedOnAnyInvalidItem(t *testing.T) {
	base := baseURL(t)
	v := reject(t, base, manifest(
		[4]string{"OK1", "3", "5", "U"},
		[4]string{"OK2", "8", "12", "L"},
		[4]string{"BAD", "3", "99", "U"},
	))
	if v.Error != "validation_failed" {
		t.Fatalf("error = %q, want validation_failed", v.Error)
	}
	if !contains(fields(v), "items[2].bay") {
		t.Fatalf("details = %+v, want field items[2].bay", v.Details)
	}
}

// TestValidationFieldPaths submits one error of every kind and checks that
// each reported field path points at the offending item and attribute.
func TestValidationFieldPaths(t *testing.T) {
	base := baseURL(t)
	body := `{"items":[
		{"cargo_id":"","hazard_class":"3","bay":5,"deck":"U"},
		{"cargo_id":"B","hazard_class":"7","bay":5,"deck":"U"},
		{"cargo_id":"C","hazard_class":"3","bay":0,"deck":"U"},
		{"cargo_id":"D","hazard_class":"3","bay":5,"deck":"X"},
		{"cargo_id":"E","bay":5,"deck":"U"}
	]}`
	v := reject(t, base, body)
	got := fields(v)
	for _, want := range []string{
		"items[0].cargo_id",
		"items[1].hazard_class",
		"items[2].bay",
		"items[3].deck",
		"items[4].hazard_class",
	} {
		if !contains(got, want) {
			t.Fatalf("details = %+v, missing field %s", v.Details, want)
		}
	}
	for _, d := range v.Details {
		if d.Message == "" {
			t.Fatalf("field %s has empty message", d.Field)
		}
	}
}

func TestDuplicateCargoID(t *testing.T) {
	base := baseURL(t)
	v := reject(t, base, manifest(
		[4]string{"SAME", "3", "5", "U"},
		[4]string{"OTHER", "8", "9", "L"},
		[4]string{"SAME", "4.1", "12", "U"},
	))
	if !contains(fields(v), "items[2].cargo_id") {
		t.Fatalf("details = %+v, want field items[2].cargo_id", v.Details)
	}
}

// TestBayBoundaries checks the bay range edges and non-integer forms.
func TestBayBoundaries(t *testing.T) {
	base := baseURL(t)
	for _, tc := range []struct {
		bay  string
		want int
	}{
		{"1", http.StatusOK},
		{"30", http.StatusOK},
		{"0", http.StatusBadRequest},
		{"31", http.StatusBadRequest},
		{"2.5", http.StatusBadRequest},
		{`"5"`, http.StatusBadRequest},
		{"true", http.StatusBadRequest},
	} {
		t.Run("bay_"+tc.bay, func(t *testing.T) {
			body := `{"items":[{"cargo_id":"X","hazard_class":"3","bay":` + tc.bay + `,"deck":"U"}]}`
			status, _ := postManifest(t, base, body)
			if status != tc.want {
				t.Fatalf("bay %s: status = %d, want %d", tc.bay, status, tc.want)
			}
		})
	}
}

// TestHazardClassForms accepts canonical strings and equivalent numbers, and
// rejects anything else.
func TestHazardClassForms(t *testing.T) {
	base := baseURL(t)
	for _, tc := range []struct {
		class string
		want  int
	}{
		{`"2.1"`, http.StatusOK},
		{`"5.1"`, http.StatusOK},
		{`2.1`, http.StatusOK},
		{`1`, http.StatusOK},
		{`"7"`, http.StatusBadRequest},
		{`2.2`, http.StatusBadRequest},
		{`"2.10"`, http.StatusBadRequest},
		{`null`, http.StatusBadRequest},
	} {
		t.Run("class_"+tc.class, func(t *testing.T) {
			body := `{"items":[{"cargo_id":"X","hazard_class":` + tc.class + `,"bay":5,"deck":"U"}]}`
			status, _ := postManifest(t, base, body)
			if status != tc.want {
				t.Fatalf("hazard_class %s: status = %d, want %d", tc.class, status, tc.want)
			}
		})
	}
}

func TestDeckValues(t *testing.T) {
	base := baseURL(t)
	for _, tc := range []struct {
		deck string
		want int
	}{
		{`"U"`, http.StatusOK},
		{`"L"`, http.StatusOK},
		{`"u"`, http.StatusBadRequest},
		{`"M"`, http.StatusBadRequest},
		{`1`, http.StatusBadRequest},
	} {
		t.Run("deck_"+tc.deck, func(t *testing.T) {
			body := `{"items":[{"cargo_id":"X","hazard_class":"3","bay":5,"deck":` + tc.deck + `}]}`
			status, _ := postManifest(t, base, body)
			if status != tc.want {
				t.Fatalf("deck %s: status = %d, want %d", tc.deck, status, tc.want)
			}
		})
	}
}

func TestMissingAndEmptyItems(t *testing.T) {
	base := baseURL(t)
	for _, body := range []string{`{}`, `{"items":[]}`, `{"items":null}`, `{"items":{}}`} {
		t.Run(body, func(t *testing.T) {
			v := reject(t, base, body)
			if !contains(fields(v), "items") {
				t.Fatalf("details = %+v, want field items", v.Details)
			}
		})
	}
}

func TestMalformedJSON(t *testing.T) {
	base := baseURL(t)
	for _, body := range []string{`{`, `not json`, `{"items":[...]}`, `{"items":[]} trailing`} {
		t.Run(body, func(t *testing.T) {
			status, data := postManifest(t, base, body)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", status)
			}
			var v validationFailure
			if err := json.Unmarshal(data, &v); err != nil {
				t.Fatalf("response is not JSON: %s", data)
			}
			if v.Error != "invalid_json" && v.Error != "validation_failed" {
				t.Fatalf("error = %q, want invalid_json or validation_failed", v.Error)
			}
		})
	}
}

// TestCheckedPairsCount verifies the pair count over the wire.
func TestCheckedPairsCount(t *testing.T) {
	base := baseURL(t)
	out := adjudicate(t, base, manifest(
		[4]string{"A", "3", "1", "U"},
		[4]string{"B", "4.1", "10", "L"},
		[4]string{"C", "8", "20", "U"},
		[4]string{"D", "2.1", "30", "L"},
	))
	if out.CheckedPairs != 6 {
		t.Fatalf("checked_pairs = %d, want 6", out.CheckedPairs)
	}
	if !out.Release {
		t.Fatalf("release = false, want true; conflicts: %+v", out.Conflicts)
	}
}

// TestRelocationPreviewResolvesConflict moves a container off the upper deck
// and expects the only conflict of the manifest to disappear. The preview's
// "before" half must equal the validate verdict for the same manifest.
func TestRelocationPreviewResolvesConflict(t *testing.T) {
	base := baseURL(t)
	items := [][4]string{
		{"ACID", "8", "5", "U"},
		{"GAS", "2.1", "7", "U"}, // same deck as ACID -> conflict
		{"NEUT", "3", "20", "L"},
	}
	out := preview(t, base, previewBody(items, "GAS", 7, "L"))

	// The "before" half must equal the validate response for the same list.
	want := adjudicate(t, base, manifest(items...))
	if !reflect.DeepEqual(out.Before, want) {
		t.Fatalf("before = %+v, want the validate response %+v", out.Before, want)
	}
	if out.Before.Release {
		t.Fatal("before.release = true, want false")
	}
	if !out.After.Release {
		t.Fatalf("after.release = false, want true; conflicts: %+v", out.After.Conflicts)
	}
	if out.After.CheckedPairs != 3 {
		t.Fatalf("after.checked_pairs = %d, want 3", out.After.CheckedPairs)
	}
	if len(out.ResolvedConflicts) != 1 || out.ResolvedConflicts[0].Pair != [2]string{"ACID", "GAS"} {
		t.Fatalf("resolved_conflicts = %+v, want exactly the ACID/GAS pair", out.ResolvedConflicts)
	}
	if got := out.ResolvedConflicts[0].Rule; got != "CORROSIVE_FLAMMABLE_GAS_SEPARATION" {
		t.Fatalf("rule = %q, want CORROSIVE_FLAMMABLE_GAS_SEPARATION", got)
	}
	if len(out.IntroducedConflicts) != 0 {
		t.Fatalf("introduced_conflicts = %+v, want empty", out.IntroducedConflicts)
	}
}

// TestRelocationPreviewIntroducesConflict moves a class 3 container next to a
// 5.1 oxidizer and expects exactly one new conflict, described at the
// candidate position. The "after" half must equal the validate verdict for
// the moved manifest.
func TestRelocationPreviewIntroducesConflict(t *testing.T) {
	base := baseURL(t)
	items := [][4]string{
		{"OXI", "5.1", "10", "U"},
		{"FLAM", "3", "20", "L"},
	}
	out := preview(t, base, previewBody(items, "FLAM", 11, "L"))

	if !out.Before.Release {
		t.Fatalf("before.release = false, want true; conflicts: %+v", out.Before.Conflicts)
	}
	if out.After.Release {
		t.Fatal("after.release = true, want false")
	}
	// The "after" half must equal the validate response of the moved list.
	moved := adjudicate(t, base, manifest(
		[4]string{"OXI", "5.1", "10", "U"},
		[4]string{"FLAM", "3", "11", "L"},
	))
	if !reflect.DeepEqual(out.After, moved) {
		t.Fatalf("after = %+v, want the validate response %+v", out.After, moved)
	}
	if len(out.ResolvedConflicts) != 0 {
		t.Fatalf("resolved_conflicts = %+v, want empty", out.ResolvedConflicts)
	}
	if len(out.IntroducedConflicts) != 1 {
		t.Fatalf("introduced_conflicts = %+v, want exactly 1", out.IntroducedConflicts)
	}
	c := out.IntroducedConflicts[0]
	if c.Pair != [2]string{"FLAM", "OXI"} {
		t.Fatalf("pair = %v, want [FLAM OXI]", c.Pair)
	}
	if c.Rule != "OXIDIZER_BAY_SEPARATION" {
		t.Fatalf("rule = %q, want OXIDIZER_BAY_SEPARATION", c.Rule)
	}
	if !strings.Contains(c.Reason, "FLAM bay 11") {
		t.Fatalf("reason = %q, want it to reference the candidate bay 11", c.Reason)
	}
}

// TestRelocationPreviewRejectedWholesale feeds the preview endpoint an
// unknown target, illegal candidate positions and an invalid manifest: every
// case must fail with HTTP 400, an accurate field path and no partial
// adjudication in the body.
func TestRelocationPreviewRejectedWholesale(t *testing.T) {
	base := baseURL(t)
	good := [][4]string{
		{"ACID", "8", "5", "U"},
		{"GAS", "2.1", "7", "U"},
	}
	cases := []struct {
		name  string
		body  string
		field string
	}{
		{"unknown_target", previewBody(good, "GHOST", 7, "L"), "target_cargo_id"},
		{"missing_target", `{"items":[` + itemsJSON(good...) + `],"candidate":{"bay":7,"deck":"L"}}`, "target_cargo_id"},
		{"candidate_bay_out_of_range", previewBody(good, "GAS", 31, "L"), "candidate.bay"},
		{"candidate_bay_fractional", `{"items":[` + itemsJSON(good...) + `],"target_cargo_id":"GAS","candidate":{"bay":7.5,"deck":"L"}}`, "candidate.bay"},
		{"candidate_deck_invalid", previewBody(good, "GAS", 7, "X"), "candidate.deck"},
		{"candidate_missing", `{"items":[` + itemsJSON(good...) + `],"target_cargo_id":"GAS"}`, "candidate"},
		{"candidate_not_object", `{"items":[` + itemsJSON(good...) + `],"target_cargo_id":"GAS","candidate":"bay 7"}`, "candidate"},
		{"manifest_item_invalid", `{"items":[{"cargo_id":"ACID","hazard_class":"8","bay":5,"deck":"U"},` +
			`{"cargo_id":"GAS","hazard_class":"2.1","bay":0,"deck":"U"}],` +
			`"target_cargo_id":"GAS","candidate":{"bay":7,"deck":"L"}}`, "items[1].bay"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, data := postPreview(t, base, tc.body)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", status, data)
			}
			var v validationFailure
			if err := json.Unmarshal(data, &v); err != nil {
				t.Fatalf("response is not JSON: %s", data)
			}
			if v.Error != "validation_failed" {
				t.Fatalf("error = %q, want validation_failed", v.Error)
			}
			if !contains(fields(v), tc.field) {
				t.Fatalf("details = %+v, want field %s", v.Details, tc.field)
			}
			// A rejected request must not carry any adjudication halves.
			for _, key := range []string{`"before"`, `"after"`, `"resolved_conflicts"`, `"introduced_conflicts"`} {
				if strings.Contains(string(data), key) {
					t.Fatalf("400 body must not contain %s: %s", key, data)
				}
			}
		})
	}
}

// TestRelocationPreviewMalformedJSON posts a truncated body to the preview
// endpoint and expects the same invalid_json shape as the validate endpoint.
func TestRelocationPreviewMalformedJSON(t *testing.T) {
	base := baseURL(t)
	status, data := postPreview(t, base, `{"items":`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	var v validationFailure
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("response is not JSON: %s", data)
	}
	if v.Error != "invalid_json" {
		t.Fatalf("error = %q, want invalid_json", v.Error)
	}
}

// TestRelocationPreviewOrderIndependent submits the same manifest and move in
// two different item orders and requires byte-identical preview responses.
func TestRelocationPreviewOrderIndependent(t *testing.T) {
	base := baseURL(t)
	forward := [][4]string{
		{"DELTA", "1", "4", "U"},
		{"ALPHA", "5.1", "10", "U"},
		{"CHARLIE", "3", "11", "L"},
		{"BRAVO", "8", "20", "U"},
		{"ECHO", "2.1", "22", "U"},
	}
	reversed := [][4]string{
		{"ECHO", "2.1", "22", "U"},
		{"BRAVO", "8", "20", "U"},
		{"CHARLIE", "3", "11", "L"},
		{"ALPHA", "5.1", "10", "U"},
		{"DELTA", "1", "4", "U"},
	}
	status1, body1 := postPreview(t, base, previewBody(forward, "ECHO", 30, "L"))
	status2, body2 := postPreview(t, base, previewBody(reversed, "ECHO", 30, "L"))
	if status1 != http.StatusOK || status2 != http.StatusOK {
		t.Fatalf("statuses = %d, %d; want 200, 200", status1, status2)
	}
	if string(body1) != string(body2) {
		t.Fatalf("item order changed the preview:\n%s\nvs\n%s", body1, body2)
	}

	var out previewResponse
	if err := json.Unmarshal(body1, &out); err != nil {
		t.Fatal(err)
	}
	// Moving ECHO to a distant lower-deck bay only lifts the BRAVO/ECHO
	// conflict; the class-1 conflicts of DELTA remain.
	if len(out.ResolvedConflicts) != 1 || out.ResolvedConflicts[0].Pair != [2]string{"BRAVO", "ECHO"} {
		t.Fatalf("resolved_conflicts = %+v, want only [BRAVO ECHO]", out.ResolvedConflicts)
	}
	if len(out.IntroducedConflicts) != 0 {
		t.Fatalf("introduced_conflicts = %+v, want empty", out.IntroducedConflicts)
	}
	if out.After.Release {
		t.Fatal("after.release = true, want false (DELTA conflicts remain)")
	}
}
