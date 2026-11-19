package rex

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func fakeDo(t *testing.T, out string, err error) *map[string]any {
	t.Helper()
	oldI, oldD := installed, runDo
	t.Cleanup(func() { installed, runDo = oldI, oldD })
	installed = func() bool { return true }
	var spec map[string]any
	runDo = func(_ context.Context, args ...string) ([]byte, error) {
		for i, a := range args {
			if a == "--args" {
				var m map[string]string
				if e := json.Unmarshal([]byte(args[i+1]), &m); e != nil {
					t.Fatal(e)
				}
				if e := json.Unmarshal([]byte(m["spec"]), &spec); e != nil {
					t.Fatal(e)
				}
			}
		}
		return []byte(out), err
	}
	return &spec
}

func TestAskPick(t *testing.T) {
	spec := fakeDo(t, `{"ok":true,"value":"once"}`, nil)
	a, err := Ask(Prompt{Title: "t", Items: []Item{{ID: "once"}, {ID: "deny"}}})
	if err != nil || !a.Answered() || a.Value != "once" {
		t.Fatalf("got %+v %v", a, err)
	}
	if (*spec)["kind"] != "pick" || (*spec)["title"] != "t" {
		t.Errorf("spec %v", *spec)
	}
}

func TestAskInputAndMulti(t *testing.T) {
	spec := fakeDo(t, `{"ok":true,"value":"get pods"}`, nil)
	if a, _ := Ask(Prompt{Title: "x"}); a.Value != "get pods" || (*spec)["kind"] != "input" {
		t.Errorf("input: %+v %v", a, *spec)
	}
	fakeDo(t, `{"ok":true,"multi":true,"values":"a\nb"}`, nil)
	if a, _ := Ask(Prompt{Title: "x", Items: []Item{{ID: "a"}}, Multiple: true}); !reflect.DeepEqual(a.Values, []string{"a", "b"}) {
		t.Errorf("multi: %+v", a)
	}
	fakeDo(t, `{"ok":true,"multi":true,"values":""}`, nil)
	if a, _ := Ask(Prompt{Title: "x", Items: []Item{{ID: "a"}}, Multiple: true}); a.Values == nil || len(a.Values) != 0 {
		t.Errorf("multi none: %+v", a)
	}
}

func TestAskNoAnswer(t *testing.T) {
	for out, want := range map[string]string{`{"reason":"cancelled"}`: "cancelled", `{"reason":"timeout"}`: "timeout"} {
		fakeDo(t, out, nil)
		a, err := Ask(Prompt{Title: "x"})
		if err != nil || a.Answered() || a.Reason != want {
			t.Errorf("%s: %+v %v", out, a, err)
		}
	}
	for _, c := range []struct {
		out string
		err error
	}{{`{"reason":"busy"}`, nil}, {`{"reason":"unsupported"}`, nil}, {`{"reason":"no_client"}`, nil},
		{``, errors.New(`no action named "k8s_ui"`)}, {`garbage`, nil}} {
		fakeDo(t, c.out, c.err)
		if _, err := Ask(Prompt{Title: "x"}); !errors.Is(err, ErrNoPrompt) {
			t.Errorf("%q %v: want ErrNoPrompt, got %v", c.out, c.err, err)
		}
	}
	installedOld := installed
	installed = func() bool { return false }
	defer func() { installed = installedOld }()
	if _, err := Ask(Prompt{Title: "x"}); !errors.Is(err, ErrNoPrompt) {
		t.Errorf("no rex: %v", err)
	}
}
