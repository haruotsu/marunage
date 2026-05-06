package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/haruotsu/marunage/internal/source"
)

// recordedCall captures one invocation so tests can assert on the
// exact (name, args) tuple the GWSClient built.
type recordedCall struct {
	name string
	args []string
}

// scriptedRunner returns canned outputs in FIFO order. Tests that
// exercise multi-step methods (List = list + N×get) queue all expected
// responses upfront.
type scriptedRunner struct {
	calls   []recordedCall
	outputs [][]byte
	outErrs []error
	callIdx int
}

func (r *scriptedRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, recordedCall{name: name, args: append([]string(nil), args...)})
	if r.callIdx >= len(r.outputs) {
		return nil, errors.New("scripted runner: ran out of canned responses")
	}
	out, err := r.outputs[r.callIdx], r.outErrs[r.callIdx]
	r.callIdx++
	return out, err
}

func findArg(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// messageListJSON returns a gws messages.list response with the given ids.
func messageListJSON(t *testing.T, ids []string) []byte {
	t.Helper()
	type stub struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
	}
	stubs := make([]stub, len(ids))
	for i, id := range ids {
		stubs[i] = stub{ID: id, ThreadID: "t-" + id}
	}
	b, err := json.Marshal(map[string]any{"messages": stubs, "resultSizeEstimate": len(ids)})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return b
}

// messageGetJSON returns a gws messages.get response.
func messageGetJSON(t *testing.T, id, threadID, snippet string, labelIDs []string, subject, from string) []byte {
	t.Helper()
	headers := []map[string]string{
		{"name": "Subject", "value": subject},
	}
	if from != "" {
		headers = append(headers, map[string]string{"name": "From", "value": from})
	}
	b, err := json.Marshal(map[string]any{
		"id":       id,
		"threadId": threadID,
		"labelIds": labelIDs,
		"snippet":  snippet,
		"payload":  map[string]any{"headers": headers},
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return b
}

// --- G1: List command shape ---------------------------------------------------

func TestGWSListBuildsListCommand(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		outputs: [][]byte{messageListJSON(t, nil)},
		outErrs: []error{nil},
	}
	c := NewGWSClient(WithRunner(runner.run))
	if _, err := c.List(context.Background(), "is:unread"); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %d, want 1 (list only, no messages)", len(runner.calls))
	}
	call := runner.calls[0]
	if call.name != "gws" {
		t.Errorf("name = %q, want gws", call.name)
	}
	wantSubcmd := []string{"gmail", "users", "messages", "list"}
	for i, w := range wantSubcmd {
		if i >= len(call.args) || call.args[i] != w {
			t.Errorf("args[%d] = %q, want %q", i, call.args[i], w)
		}
	}
	params := findArg(call.args, "--params")
	if params == "" {
		t.Fatalf("--params missing: %v", call.args)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(params), &got); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	if got["userId"] != "me" {
		t.Errorf("userId = %v, want me", got["userId"])
	}
	if got["q"] != "is:unread" {
		t.Errorf("q = %v, want is:unread", got["q"])
	}
	if findArg(call.args, "--format") != "json" {
		t.Errorf("--format missing or wrong: %v", call.args)
	}
	// maxResults must be set to bound the N+1 get calls.
	if got["maxResults"] == nil {
		t.Errorf("maxResults missing from params: %v", got)
	}
}

func TestGWSListDefaultMaxResults(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		outputs: [][]byte{messageListJSON(t, nil)},
		outErrs: []error{nil},
	}
	c := NewGWSClient(WithRunner(runner.run))
	if _, err := c.List(context.Background(), "is:unread"); err != nil {
		t.Fatalf("List: %v", err)
	}
	params := findArg(runner.calls[0].args, "--params")
	var got map[string]any
	if err := json.Unmarshal([]byte(params), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	maxResults, ok := got["maxResults"].(float64)
	if !ok || maxResults <= 0 {
		t.Errorf("maxResults = %v, want positive number", got["maxResults"])
	}
}

func TestGWSListWithMaxResultsOption(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		outputs: [][]byte{messageListJSON(t, nil)},
		outErrs: []error{nil},
	}
	c := NewGWSClient(WithRunner(runner.run), WithMaxResults(10))
	if _, err := c.List(context.Background(), "is:unread"); err != nil {
		t.Fatalf("List: %v", err)
	}
	params := findArg(runner.calls[0].args, "--params")
	var got map[string]any
	if err := json.Unmarshal([]byte(params), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["maxResults"] != float64(10) {
		t.Errorf("maxResults = %v, want 10", got["maxResults"])
	}
}

// --- G2: List issues get per message -----------------------------------------

func TestGWSListBuildsGetCommandPerMessage(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		outputs: [][]byte{
			messageListJSON(t, []string{"msg1"}),
			messageGetJSON(t, "msg1", "t-msg1", "preview", []string{"INBOX"}, "Hello", ""),
		},
		outErrs: []error{nil, nil},
	}
	c := NewGWSClient(WithRunner(runner.run))
	if _, err := c.List(context.Background(), "is:unread"); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls = %d, want 2 (list + 1 get)", len(runner.calls))
	}
	getCall := runner.calls[1]
	wantSubcmd := []string{"gmail", "users", "messages", "get"}
	for i, w := range wantSubcmd {
		if i >= len(getCall.args) || getCall.args[i] != w {
			t.Errorf("get args[%d] = %q, want %q", i, getCall.args[i], w)
		}
	}
	params := findArg(getCall.args, "--params")
	var got map[string]any
	if err := json.Unmarshal([]byte(params), &got); err != nil {
		t.Fatalf("decode get params: %v", err)
	}
	if got["userId"] != "me" {
		t.Errorf("userId = %v", got["userId"])
	}
	if got["id"] != "msg1" {
		t.Errorf("id = %v, want msg1", got["id"])
	}
	if got["format"] != "metadata" {
		t.Errorf("format = %v, want metadata", got["format"])
	}
}

// --- G3: Parse message fields -------------------------------------------------

func TestGWSListParsesSubjectSnippetAndLabels(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		outputs: [][]byte{
			messageListJSON(t, []string{"m1"}),
			messageGetJSON(t, "m1", "t1", "body preview", []string{"INBOX", "UNREAD"}, "My Subject", "sender@example.com"),
		},
		outErrs: []error{nil, nil},
	}
	c := NewGWSClient(WithRunner(runner.run))
	msgs, err := c.List(context.Background(), "is:unread")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len = %d, want 1", len(msgs))
	}
	m := msgs[0]
	if m.ID != "m1" {
		t.Errorf("ID = %q", m.ID)
	}
	if m.ThreadID != "t1" {
		t.Errorf("ThreadID = %q", m.ThreadID)
	}
	if m.Subject != "My Subject" {
		t.Errorf("Subject = %q", m.Subject)
	}
	if m.Snippet != "body preview" {
		t.Errorf("Snippet = %q", m.Snippet)
	}
	if m.From != "sender@example.com" {
		t.Errorf("From = %q", m.From)
	}
	if len(m.Labels) != 2 || m.Labels[0] != "INBOX" || m.Labels[1] != "UNREAD" {
		t.Errorf("Labels = %v", m.Labels)
	}
}

// --- G4: Empty list returns empty slice ---------------------------------------

func TestGWSListEmptyReturnsEmptySlice(t *testing.T) {
	t.Parallel()

	// gws returns {} (no "messages" key) when the inbox is empty.
	runner := &scriptedRunner{
		outputs: [][]byte{[]byte(`{}`)},
		outErrs: []error{nil},
	}
	c := NewGWSClient(WithRunner(runner.run))
	msgs, err := c.List(context.Background(), "is:unread")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("len = %d, want 0", len(msgs))
	}
	if len(runner.calls) != 1 {
		t.Errorf("calls = %d, want 1 (no get calls when list is empty)", len(runner.calls))
	}
}

// --- G5: newer_than appended to query ----------------------------------------

func TestGWSListAppendsNewerThanToQuery(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		outputs: [][]byte{messageListJSON(t, nil)},
		outErrs: []error{nil},
	}
	c := NewGWSClient(WithRunner(runner.run), WithNewerThan(7))
	if _, err := c.List(context.Background(), "is:unread"); err != nil {
		t.Fatalf("List: %v", err)
	}
	params := findArg(runner.calls[0].args, "--params")
	var got map[string]any
	if err := json.Unmarshal([]byte(params), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	q, _ := got["q"].(string)
	if !strings.Contains(q, "newer_than:7d") {
		t.Errorf("q = %q, want it to contain newer_than:7d", q)
	}
}

func TestGWSListZeroNewerThanDoesNotAppend(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		outputs: [][]byte{messageListJSON(t, nil)},
		outErrs: []error{nil},
	}
	c := NewGWSClient(WithRunner(runner.run)) // default newerThanDays = 0
	if _, err := c.List(context.Background(), "is:unread"); err != nil {
		t.Fatalf("List: %v", err)
	}
	params := findArg(runner.calls[0].args, "--params")
	var got map[string]any
	if err := json.Unmarshal([]byte(params), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	q, _ := got["q"].(string)
	if strings.Contains(q, "newer_than") {
		t.Errorf("q = %q, want no newer_than when days=0", q)
	}
}

// --- G6: Error propagation ---------------------------------------------------

func TestGWSListWrapsListRunnerError(t *testing.T) {
	t.Parallel()

	upstream := errors.New("gws: exit 1")
	runner := &scriptedRunner{
		outputs: [][]byte{nil},
		outErrs: []error{upstream},
	}
	c := NewGWSClient(WithRunner(runner.run))
	_, err := c.List(context.Background(), "is:unread")
	if !errors.Is(err, upstream) {
		t.Errorf("err = %v, want wrap of upstream", err)
	}
}

func TestGWSListWrapsGetRunnerError(t *testing.T) {
	t.Parallel()

	upstream := errors.New("gws: exit 1")
	runner := &scriptedRunner{
		outputs: [][]byte{messageListJSON(t, []string{"m1"}), nil},
		outErrs: []error{nil, upstream},
	}
	c := NewGWSClient(WithRunner(runner.run))
	_, err := c.List(context.Background(), "is:unread")
	if !errors.Is(err, upstream) {
		t.Errorf("err = %v, want wrap of upstream", err)
	}
	if !strings.Contains(err.Error(), "m1") {
		t.Errorf("err = %v, want message id in error", err)
	}
}

func TestGWSListWrapsMalformedListJSON(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		outputs: [][]byte{[]byte(`{"messages": [`)},
		outErrs: []error{nil},
	}
	c := NewGWSClient(WithRunner(runner.run))
	_, err := c.List(context.Background(), "is:unread")
	if err == nil {
		t.Fatalf("want non-nil error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("err = %v, want decode mention", err)
	}
}

func TestGWSListWrapsMalformedGetJSON(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		outputs: [][]byte{messageListJSON(t, []string{"m1"}), []byte(`{bad json`)},
		outErrs: []error{nil, nil},
	}
	c := NewGWSClient(WithRunner(runner.run))
	_, err := c.List(context.Background(), "is:unread")
	if err == nil {
		t.Fatalf("want non-nil error for malformed get JSON")
	}
}

// --- G7: ModifyLabels command shape ------------------------------------------

func TestGWSModifyLabelsBuildsCommand(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		outputs: [][]byte{[]byte(`{}`)},
		outErrs: []error{nil},
	}
	c := NewGWSClient(WithRunner(runner.run))
	err := c.ModifyLabels(context.Background(), "msg123", ModifyLabelsRequest{
		AddLabels:    []string{"auto-archived"},
		RemoveLabels: []string{"UNREAD"},
	})
	if err != nil {
		t.Fatalf("ModifyLabels: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(runner.calls))
	}
	call := runner.calls[0]
	wantSubcmd := []string{"gmail", "users", "messages", "modify"}
	for i, w := range wantSubcmd {
		if i >= len(call.args) || call.args[i] != w {
			t.Errorf("args[%d] = %q, want %q", i, call.args[i], w)
		}
	}
	params := findArg(call.args, "--params")
	var gotParams map[string]any
	if err := json.Unmarshal([]byte(params), &gotParams); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	if gotParams["userId"] != "me" {
		t.Errorf("params.userId = %v", gotParams["userId"])
	}
	if gotParams["id"] != "msg123" {
		t.Errorf("params.id = %v", gotParams["id"])
	}
	body := findArg(call.args, "--json")
	var gotBody map[string]any
	if err := json.Unmarshal([]byte(body), &gotBody); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	addLabels, _ := gotBody["addLabelIds"].([]any)
	if len(addLabels) != 1 || addLabels[0] != "auto-archived" {
		t.Errorf("addLabelIds = %v", addLabels)
	}
	removeLabels, _ := gotBody["removeLabelIds"].([]any)
	if len(removeLabels) != 1 || removeLabels[0] != "UNREAD" {
		t.Errorf("removeLabelIds = %v", removeLabels)
	}
}

func TestGWSModifyLabelsWrapsRunnerError(t *testing.T) {
	t.Parallel()

	upstream := errors.New("gws: 404 not found")
	runner := &scriptedRunner{
		outputs: [][]byte{nil},
		outErrs: []error{upstream},
	}
	c := NewGWSClient(WithRunner(runner.run))
	err := c.ModifyLabels(context.Background(), "ghost", ModifyLabelsRequest{})
	if !errors.Is(err, upstream) {
		t.Errorf("err = %v, want wrap of upstream", err)
	}
}

// --- G8: AuthStatus ----------------------------------------------------------

func TestGWSAuthStatusAuthenticated(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		outputs: [][]byte{[]byte(`{"emailAddress":"me@example.com"}`)},
		outErrs: []error{nil},
	}
	c := NewGWSClient(WithRunner(runner.run))
	got, err := c.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthAuthenticated {
		t.Errorf("status = %q, want authenticated", got)
	}
	if len(runner.calls) != 1 {
		t.Errorf("calls = %d, want 1", len(runner.calls))
	}
	if runner.calls[0].name != "gws" {
		t.Errorf("name = %q, want gws", runner.calls[0].name)
	}
}

func TestGWSAuthStatusNotConfiguredOnRunnerFailure(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		outputs: [][]byte{nil},
		outErrs: []error{errors.New("gws: auth error")},
	}
	c := NewGWSClient(WithRunner(runner.run))
	got, err := c.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if got != source.AuthNotConfigured {
		t.Errorf("status = %q, want not_configured", got)
	}
}

// --- G9: Authenticate ---------------------------------------------------------

func TestGWSAuthenticateNonInteractiveReturnsError(t *testing.T) {
	t.Parallel()

	c := NewGWSClient(WithRunner(func(context.Context, string, ...string) ([]byte, error) {
		t.Fatalf("runner must not be called in non-interactive mode")
		return nil, nil
	}))
	err := c.Authenticate(context.Background(), source.SetupOptions{NonInteractive: true})
	if err == nil {
		t.Fatalf("want error for non-interactive Authenticate")
	}
	if !strings.Contains(err.Error(), "gws auth") {
		t.Errorf("err = %v, want mention of gws auth", err)
	}
}

func TestGWSAuthenticateInteractiveRunsProbe(t *testing.T) {
	t.Parallel()

	upstream := errors.New("exec: gws: not found")
	runner := &scriptedRunner{
		outputs: [][]byte{nil},
		outErrs: []error{upstream},
	}
	c := NewGWSClient(WithRunner(runner.run))
	err := c.Authenticate(context.Background(), source.SetupOptions{NonInteractive: false})
	if !errors.Is(err, upstream) {
		t.Errorf("err = %v, want wrap of upstream", err)
	}
}

func TestGWSAuthenticateInteractiveSucceedsWhenProbeOK(t *testing.T) {
	t.Parallel()

	runner := &scriptedRunner{
		outputs: [][]byte{[]byte(`{"emailAddress":"me@example.com"}`)},
		outErrs: []error{nil},
	}
	c := NewGWSClient(WithRunner(runner.run))
	if err := c.Authenticate(context.Background(), source.SetupOptions{}); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
}
