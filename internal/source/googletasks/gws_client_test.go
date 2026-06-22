package googletasks

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeGWSRunner struct {
	out   []byte
	err   error
	calls [][]string
}

func (f *fakeGWSRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	return f.out, f.err
}

func newGWSTestClient(out string, err error) (*GWSClient, *fakeGWSRunner) {
	fr := &fakeGWSRunner{out: []byte(out), err: err}
	return NewGWSClient(WithRunner(fr.run)), fr
}

func TestGWSListTaskListsStripsPrefixAndParses(t *testing.T) {
	// gws prints a "Using keyring backend" line before the JSON.
	c, fr := newGWSTestClient("Using keyring backend: keyring\n{\"items\":[{\"id\":\"L1\",\"title\":\"marunage\"}]}", nil)
	lists, err := c.ListTaskLists(context.Background())
	if err != nil {
		t.Fatalf("ListTaskLists: %v", err)
	}
	if len(lists) != 1 || lists[0].ID != "L1" || lists[0].Title != "marunage" {
		t.Fatalf("lists = %+v", lists)
	}
	if got := fr.calls[0]; got[1] != "tasks" || got[2] != "tasklists" || got[3] != "list" {
		t.Errorf("argv = %v", got)
	}
}

func TestGWSListTasksPassesTasklistAndParses(t *testing.T) {
	c, fr := newGWSTestClient(`{"items":[{"id":"T1","title":"review deploy doc","notes":"by 3pm","status":"needsAction"}]}`, nil)
	tasks, err := c.ListTasks(context.Background(), "L1")
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != "T1" || tasks[0].Notes != "by 3pm" || tasks[0].Status != "needsAction" {
		t.Fatalf("tasks = %+v", tasks)
	}
	params := argValue(fr.calls[0], "--params")
	if !strings.Contains(params, `"tasklist":"L1"`) || !strings.Contains(params, `"showCompleted":true`) {
		t.Errorf("params = %q", params)
	}
}

func TestGWSInsertTaskSendsTitleAndNotes(t *testing.T) {
	c, fr := newGWSTestClient(`{"id":"T9","title":"new","notes":"n","status":"needsAction"}`, nil)
	got, err := c.InsertTask(context.Background(), "L1", GTask{Title: "new", Notes: "n"})
	if err != nil {
		t.Fatalf("InsertTask: %v", err)
	}
	if got.ID != "T9" {
		t.Errorf("got = %+v", got)
	}
	body := argValue(fr.calls[0], "--json")
	if !strings.Contains(body, `"title":"new"`) || !strings.Contains(body, `"notes":"n"`) {
		t.Errorf("body = %q", body)
	}
}

func TestGWSPatchTaskOmitsEmptyFields(t *testing.T) {
	c, fr := newGWSTestClient(`{"id":"T1","title":"t","status":"completed"}`, nil)
	if _, err := c.PatchTask(context.Background(), "L1", "T1", GTask{Status: statusCompleted}); err != nil {
		t.Fatalf("PatchTask: %v", err)
	}
	body := argValue(fr.calls[0], "--json")
	if !strings.Contains(body, `"status":"completed"`) {
		t.Errorf("body = %q, want status", body)
	}
	if strings.Contains(body, `"title"`) || strings.Contains(body, `"notes"`) {
		t.Errorf("body = %q should omit empty title/notes", body)
	}
}

func TestGWSDeleteTaskArgs(t *testing.T) {
	c, fr := newGWSTestClient("", nil)
	if err := c.DeleteTask(context.Background(), "L1", "T1"); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	got := fr.calls[0]
	if got[2] != "tasks" || got[3] != "delete" {
		t.Errorf("argv = %v", got)
	}
	params := argValue(got, "--params")
	if !strings.Contains(params, `"task":"T1"`) || !strings.Contains(params, `"tasklist":"L1"`) {
		t.Errorf("params = %q", params)
	}
}

func TestGWSUnauthorizedPropagates(t *testing.T) {
	c, _ := newGWSTestClient("", ErrUnauthorized)
	_, err := c.ListTaskLists(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestJSONBodyStripsPrefix(t *testing.T) {
	if got := string(jsonBody([]byte("noise here\n[1,2]"))); got != "[1,2]" {
		t.Errorf("jsonBody = %q", got)
	}
	if got := string(jsonBody([]byte(`{"a":1}`))); got != `{"a":1}` {
		t.Errorf("jsonBody = %q", got)
	}
}

func argValue(argv []string, flag string) string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag {
			return argv[i+1]
		}
	}
	return ""
}
